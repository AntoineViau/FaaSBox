package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
)

func main() {
	initEncryptionKey()

	// Read before the application is built, like the encryption key: this one
	// decides whether every write route of the instance is reachable at all,
	// and a value nobody can parse must not fall back to a mode.
	demo, demoErr := demoSettingsFromEnv()
	if demoErr != nil {
		log.Fatalf("faasbox: %v", demoErr)
	}

	app := pocketbase.New()

	var functionsDir string
	app.RootCmd.PersistentFlags().StringVar(
		&functionsDir,
		"functionsDir",
		defaultFunctionsDir(),
		"the directory containing the TypeScript function folders",
	)

	// Derive pb_public path from DataDir (sibling of pb_data inside data/)
	publicDir := func() string {
		return filepath.Join(filepath.Dir(app.DataDir()), "pb_public")
	}

	// Context cancelled on server shutdown — stops in-flight cron functions and
	// background dependency installs instead of leaving them orphaned.
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())

	app.OnTerminate().Bind(&hook.Handler[*core.TerminateEvent]{
		Id: "faasboxCancelLifecycleCtx",
		Func: func(e *core.TerminateEvent) error {
			lifecycleCancel()
			return e.Next()
		},
	})

	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Func: func(e *core.ServeEvent) error {
			// A showcase refuses every write, and the refusal is posed here —
			// on the root router, ahead of any route registration. That is not
			// negotiable: the editor writes *through* /api/collections/..., so
			// a refusal that did not cover PocketBase's own routes and the
			// admin at /_/ would refuse nothing at all.
			if demo.Enabled {
				e.Router.BindFunc(refuseWrites)
			}

			// Ensure collections exist. Functions first: the cron jobs and the
			// logs carry a relation to it, and a relation field needs the id of
			// the collection it points at.
			if err := ensureFunctionsCollection(e.App); err != nil {
				return fmt.Errorf("failed to create %s collection: %w", faasboxFunctionsCollection, err)
			}
			if err := ensureAPIKeysCollection(e.App); err != nil {
				return fmt.Errorf("failed to create %s collection: %w", faasboxAPIKeysCollection, err)
			}
			if err := ensureCronJobsCollection(e.App); err != nil {
				return fmt.Errorf("failed to create %s collection: %w", faasboxCronJobsCollection, err)
			}
			if err := ensureLogsCollection(e.App); err != nil {
				return fmt.Errorf("failed to create %s collection: %w", faasboxLogsCollection, err)
			}

			// Arm PocketBase's own rate limiter, off by default. Same reason
			// the collections are created here: a setting that lives in the
			// database has to be posed by code, or a fresh one comes up
			// without it. A server that starts announcing a limiter it failed
			// to arm is worse than one with none.
			if err := ensureRateLimits(e.App); err != nil {
				return fmt.Errorf("failed to enable the rate limiter: %w", err)
			}

			// Restore functions from DB to disk (recreate files after container
			// restart). It runs in a showcase too: it writes to disk and not to
			// the database, and it is what fills the folder the Files tab shows.
			syncDiskFromDB(e.App, functionsDir)

			// The middleware above closes the caller; it says nothing about what
			// the server starts for its own account, and a showcase starts none
			// of it. Each of the four writes to the database on its own, for
			// something nothing will ever call: an install stamps its state, a
			// scheduler would execute code and log the run, the missed-run report
			// files `missed` entries for a scheduler that is not running, and the
			// hourly prune would delete the very history the showcase displays.
			if !demo.Enabled {
				// Reinstall the dependencies the disk lost, from the files just restored.
				// Detached: OnServe runs before the server listens, so anything synchronous
				// here delays the first response — and bun takes its time.
				go installMissingDeps(lifecycleCtx, e.App, functionsDir)

				// Load existing cron jobs
				syncAllCronJobs(e.App, functionsDir, lifecycleCtx)

				// Report the triggers that were due while the server was down
				reportMissedCronRuns(e.App, time.Now())

				// Internal hourly cron: prunes the logs, and behind them the OAuth
				// registrations and grants that /oauth/register lets anyone create.
				// The OAuth pass returns silently when those collections are absent.
				e.App.Cron().Add("__faasboxLogPrune", "0 * * * *", func() {
					pruneOldLogs(e.App)
					pruneOAuthRecords(e.App)
				})
			}

			// Health check (public, no API key)
			e.Router.GET("/health", func(re *core.RequestEvent) error {
				if _, err := e.App.FindCollectionByNameOrId(faasboxLogsCollection); err != nil {
					return re.JSON(http.StatusServiceUnavailable, map[string]string{
						"status": "unhealthy",
						"error":  "database not accessible",
					})
				}
				return re.JSON(http.StatusOK, map[string]string{"status": "ok"})
			})

			// What mode this instance runs in (public, both modes). The sign-in
			// form reads it to fill itself in on a showcase.
			e.Router.GET("/api/faasbox/instance", instanceHandler(demo))

			// What this instance is called from outside, read once: it says
			// whether the OAuth authorization server goes up at all, and it is
			// what /mcp is handed so a token opens it and its 401 says where
			// to go and get one. Absent or invalid, the reason is printed and
			// the zero configuration leaves every route on API keys alone.
			oauth, oauthErr := oauthConfigFromEnv()
			if oauthErr != nil {
				reportOAuthDisabled(oauthErr)
				oauth = apiKeysOnly
			}

			// Routes protected by API key
			faas := e.Router.Group("")
			faas.Bind(requireAPIKey(e.App, apiKeysOnly))
			faas.POST("/invoke/{name}", func(re *core.RequestEvent) error {
				return invokeHandler(re, functionsDir)
			})
			faas.GET("/functions", func(re *core.RequestEvent) error {
				return listFunctionsHandler(re, functionsDir)
			})

			// Function management. Behind the same key middleware, plus the
			// canManage guard: reaching a function and rewriting it are two
			// different rights, and only the second lets a key decide what this
			// server executes.
			manage := e.Router.Group("/api/faasbox/functions")
			manage.Bind(requireAPIKey(e.App, apiKeysOnly))
			manage.Bind(requireManageKey())
			manage.POST("", createFunctionHandler)
			manage.GET("/{name}", getFunctionHandler)
			manage.PUT("/{name}", replaceFunctionHandler)
			manage.DELETE("/{name}", deleteFunctionHandler)
			// Reading the history needs that same right: a log carries the
			// payload and the output of runs the key holder never triggered.
			manage.GET("/{name}/logs", functionLogsHandler)

			// The MCP server, behind the same two middlewares — writing a
			// function through a tool is the same right as writing it through
			// the route — plus exposeKeyScope, which carries the key onto the
			// standard request context the wrapped handler receives.
			//
			// It is the one group handed the OAuth configuration: an access
			// token opens it, and its 401 carries the signpost that starts the
			// flow. Every other route above stays on API keys alone.
			//
			// Mounted through the router rather than beside it: a handler
			// registered outside this group would answer without a key. GET is
			// mounted so a browser hitting /mcp gets the transport's own 405
			// instead of the SPA the catch-all below would serve it.
			mcpRoutes := e.Router.Group("/mcp")
			mcpRoutes.Bind(requireAPIKey(e.App, oauth))
			mcpRoutes.Bind(requireManageKey())
			mcpRoutes.Bind(exposeKeyScope())
			mcpServe := apis.WrapStdHandler(mcpHandler(e.App, functionsDir))
			mcpRoutes.POST("", mcpServe)
			mcpRoutes.GET("", mcpServe)

			// The OAuth authorization server, mounted only when
			// FAASBOX_PUBLIC_URL said what this instance is called from
			// outside: a server that cannot name itself cannot publish a
			// discovery document. Absent or invalid, nothing goes up — /mcp
			// keeps answering to an API key, and to nothing else.
			if oauth != apiKeysOnly {
				if err := mountOAuth(e, oauth); err != nil {
					return err
				}
			}

			// Key management (superuser only, no API key needed)
			e.Router.POST("/api/faasbox/keys", func(re *core.RequestEvent) error {
				return createKeyHandler(re)
			}).Bind(apis.RequireSuperuserAuth())

			// Decrypted environment of a function (superuser only)
			e.Router.GET("/api/faasbox/functions/{name}/env", func(re *core.RequestEvent) error {
				return functionEnvHandler(re)
			}).Bind(apis.RequireSuperuserAuth())

			// One level of a function's directory on disk (superuser only)
			e.Router.GET("/api/faasbox/functions/{name}/files", func(re *core.RequestEvent) error {
				return functionFilesHandler(re, functionsDir)
			}).Bind(apis.RequireSuperuserAuth())

			// Content of one file in that directory (superuser only)
			e.Router.GET("/api/faasbox/functions/{name}/files/content", func(re *core.RequestEvent) error {
				return functionFileContentHandler(re, functionsDir)
			}).Bind(apis.RequireSuperuserAuth())

			// serve static files from pb_public (Angular SPA + assets)
			if !e.Router.HasRoute(http.MethodGet, "/{path...}") {
				e.Router.GET("/{path...}", apis.Static(os.DirFS(publicDir()), true))
			}

			// Print FaaSBox banner after PocketBase's own startup message
			addr := e.Server.Addr
			if addr == "" {
				addr = "http://127.0.0.1:8080"
			}
			fmt.Println("")
			fmt.Println("  ╔══════════════════════════════════════╗")
			fmt.Printf("  ║  FaaSBox is ready at %-16s ║\n", addr)
			fmt.Println("  ╚══════════════════════════════════════╝")
			fmt.Println("")

			return e.Next()
		},
		Priority: -10, // register early so other hooks can override
	})

	// Default active=true for API keys created via admin UI
	app.OnRecordCreate(faasboxAPIKeysCollection).BindFunc(func(e *core.RecordEvent) error {
		if !e.Record.GetBool("active") {
			e.Record.Set("active", true)
		}
		return e.Next()
	})

	// Every validation below runs on what the caller submitted, which is to say
	// on plaintext, and every one of them is therefore bound *before*
	// registerFieldEncryption. A validator handed a sealed value weighs base64 —
	// the cron expression and the function name both.
	//
	// Refusing early also spares an AES pass on a record that will not be
	// written.
	app.OnRecordCreate(faasboxFunctionsCollection).BindFunc(validateFunctionNameHook)
	app.OnRecordUpdate(faasboxFunctionsCollection).BindFunc(validateFunctionNameHook)

	// The source limit the product announces, now that the column measures the
	// ciphertext and no longer holds it.
	app.OnRecordCreate(faasboxFunctionsCollection).BindFunc(validateFunctionSizeHook)
	app.OnRecordUpdate(faasboxFunctionsCollection).BindFunc(validateFunctionSizeHook)

	// Encrypt plainEnv before saving faasbox_functions records
	app.OnRecordCreate(faasboxFunctionsCollection).BindFunc(encryptPlainEnvHook)
	app.OnRecordUpdate(faasboxFunctionsCollection).BindFunc(encryptPlainEnvHook)

	// Validate cron expression before saving
	app.OnRecordCreate(faasboxCronJobsCollection).BindFunc(validateCronScheduleHook)
	app.OnRecordUpdate(faasboxCronJobsCollection).BindFunc(validateCronScheduleHook)

	// Stamp the fingerprint of the two columns SQL still has to find. Before the
	// encryption, so the value hashed is the one the caller sent.
	registerBlindIndexes(app)

	// Encrypt the business columns of every collection at rest, and decrypt them
	// for the responses the editor reads. Bound last, for the ordering above.
	registerFieldEncryption(app)

	// Live-sync cron jobs when records change
	app.OnRecordAfterCreateSuccess(faasboxCronJobsCollection).BindFunc(func(e *core.RecordEvent) error {
		syncAllCronJobs(e.App, functionsDir, lifecycleCtx)
		return e.Next()
	})
	app.OnRecordAfterUpdateSuccess(faasboxCronJobsCollection).BindFunc(func(e *core.RecordEvent) error {
		syncAllCronJobs(e.App, functionsDir, lifecycleCtx)
		return e.Next()
	})
	app.OnRecordAfterDeleteSuccess(faasboxCronJobsCollection).BindFunc(func(e *core.RecordEvent) error {
		syncAllCronJobs(e.App, functionsDir, lifecycleCtx)
		return e.Next()
	})

	// Live-sync function code to disk and install its dependencies when records change
	// functionsDir is read inside the handler, not when the hook is bound: the
	// flag still holds its default at this point.
	app.OnRecordAfterCreateSuccess(faasboxFunctionsCollection).BindFunc(func(e *core.RecordEvent) error {
		return syncFunctionRecord(lifecycleCtx, e, functionsDir)
	})
	app.OnRecordAfterUpdateSuccess(faasboxFunctionsCollection).BindFunc(func(e *core.RecordEvent) error {
		return syncFunctionRecord(lifecycleCtx, e, functionsDir)
	})
	app.OnRecordAfterDeleteSuccess(faasboxFunctionsCollection).BindFunc(func(e *core.RecordEvent) error {
		if err := deleteRecordFromDisk(e.Record, functionsDir); err != nil {
			e.App.Logger().Error("faasbox: failed to delete function from disk",
				"function", functionName(e.App, e.Record), "error", err)
		}
		return e.Next()
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}

func defaultFunctionsDir() string {
	return filepath.Join(".", "functions")
}

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
			// Ensure collections exist
			if err := ensureAPIKeysCollection(e.App); err != nil {
				return fmt.Errorf("failed to create %s collection: %w", faasboxAPIKeysCollection, err)
			}
			if err := ensureCronJobsCollection(e.App); err != nil {
				return fmt.Errorf("failed to create %s collection: %w", faasboxCronJobsCollection, err)
			}
			if err := ensureLogsCollection(e.App); err != nil {
				return fmt.Errorf("failed to create %s collection: %w", faasboxLogsCollection, err)
			}
			if err := ensureFunctionsCollection(e.App); err != nil {
				return fmt.Errorf("failed to create %s collection: %w", faasboxFunctionsCollection, err)
			}

			// Restore functions from DB to disk (recreate files after container restart)
			syncDiskFromDB(e.App, functionsDir)

			// Reinstall the dependencies the disk lost, from the files just restored.
			// Detached: OnServe runs before the server listens, so anything synchronous
			// here delays the first response — and bun takes its time.
			go installMissingDeps(lifecycleCtx, e.App, functionsDir)

			// Load existing cron jobs
			syncAllCronJobs(e.App, functionsDir, lifecycleCtx)

			// Report the triggers that were due while the server was down
			reportMissedCronRuns(e.App, time.Now())

			// Internal cron to prune old logs every hour
			e.App.Cron().Add("__faasboxLogPrune", "0 * * * *", func() {
				pruneOldLogs(e.App)
			})

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

			// Routes protected by API key
			faas := e.Router.Group("")
			faas.Bind(requireAPIKey(e.App))
			faas.POST("/invoke/{name}", func(re *core.RequestEvent) error {
				return invokeHandler(re, functionsDir)
			})
			faas.GET("/functions", func(re *core.RequestEvent) error {
				return listFunctionsHandler(re, functionsDir)
			})

			// Key management (superuser only, no API key needed)
			e.Router.POST("/api/faasbox/keys", func(re *core.RequestEvent) error {
				return createKeyHandler(re)
			}).Bind(apis.RequireSuperuserAuth())

			// Decrypted environment of a function (superuser only)
			e.Router.GET("/api/faasbox/functions/{name}/env", func(re *core.RequestEvent) error {
				return functionEnvHandler(re)
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

	// Encrypt plainEnv before saving faasbox_functions records
	app.OnRecordCreate(faasboxFunctionsCollection).BindFunc(encryptPlainEnvHook)
	app.OnRecordUpdate(faasboxFunctionsCollection).BindFunc(encryptPlainEnvHook)

	// Validate cron expression before saving
	app.OnRecordCreate(faasboxCronJobsCollection).BindFunc(validateCronScheduleHook)
	app.OnRecordUpdate(faasboxCronJobsCollection).BindFunc(validateCronScheduleHook)

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
				"function", e.Record.GetString("name"), "error", err)
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

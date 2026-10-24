package main

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// seed is the pair the scenarios below start the instance with.
var seed = superuserSettings{Email: "admin@example.com", Password: "password-one"}

// freshApp is an instance with no superuser at all. The default test data
// carries several, so the scenarios start from an empty data directory: the
// migrations are replayed onto it and nothing else.
func freshApp(t testing.TB) *tests.TestApp {
	t.Helper()

	app, err := tests.NewTestApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)

	if total := countSuperusers(t, app); total != 0 {
		t.Fatalf("a fresh app carries %d superusers, want none", total)
	}
	return app
}

// seededApp is a fresh instance whose first start the variables seeded.
func seededApp(t testing.TB) *tests.TestApp {
	t.Helper()

	app := freshApp(t)
	ensureSuperuser(app, seed)
	findSuperuser(t, app, seed.Email)
	return app
}

// captureLog collects what the standard logger prints until the test ends,
// which is where every line of the seeding goes.
func captureLog(t testing.TB) *bytes.Buffer {
	t.Helper()

	var out bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &out
}

func countSuperusers(t testing.TB, app core.App) int64 {
	t.Helper()

	total, err := app.CountRecords(core.CollectionNameSuperusers)
	if err != nil {
		t.Fatal(err)
	}
	return total
}

func findSuperuser(t testing.TB, app core.App, email string) *core.Record {
	t.Helper()

	record, err := app.FindAuthRecordByEmail(core.CollectionNameSuperusers, email)
	if err != nil {
		t.Fatalf("no superuser %s: %v", email, err)
	}
	return record
}

// editSuperuser is what the admin does when the account is edited there.
func editSuperuser(t testing.TB, app core.App, record *core.Record, edit func(*core.Record)) {
	t.Helper()

	edit(record)
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
}

// assertLine checks that the captured output carries every wanted fragment, and
// never the password of the variables.
func assertLine(t testing.TB, out *bytes.Buffer, password string, want ...string) {
	t.Helper()

	line := out.String()
	for _, w := range want {
		if !strings.Contains(line, w) {
			t.Errorf("log %q does not carry %q", line, w)
		}
	}
	if strings.Contains(line, password) {
		t.Errorf("log %q carries the password", line)
	}
}

// TestSuperuserSeedsAFreshInstance covers the first start: the account is
// created from the variables, and nothing is said about it.
func TestSuperuserSeedsAFreshInstance(t *testing.T) {
	app := freshApp(t)
	out := captureLog(t)

	ensureSuperuser(app, seed)

	record := findSuperuser(t, app, seed.Email)
	if !record.ValidatePassword(seed.Password) {
		t.Error("the seeded password does not open the account")
	}
	if total := countSuperusers(t, app); total != 1 {
		t.Errorf("%d superusers, want 1", total)
	}
	if out.Len() != 0 {
		t.Errorf("seeding a fresh instance printed %q, want nothing", out.String())
	}
}

// TestSuperuserSeedsPastTheInstaller covers an instance started once without
// the variables: PocketBase left its installer account behind, which must
// neither count as an account nor survive the real one.
func TestSuperuserSeedsPastTheInstaller(t *testing.T) {
	app := freshApp(t)

	installer, err := newSuperuser(app)
	if err != nil {
		t.Fatal(err)
	}
	installer.SetEmail(core.DefaultInstallerEmail)
	installer.SetRandomPassword()
	if err := app.Save(installer); err != nil {
		t.Fatal(err)
	}

	ensureSuperuser(app, seed)

	if !findSuperuser(t, app, seed.Email).ValidatePassword(seed.Password) {
		t.Error("the seeded password does not open the account")
	}
	if _, err := app.FindAuthRecordByEmail(core.CollectionNameSuperusers, core.DefaultInstallerEmail); err == nil {
		t.Error("the installer account survived the seeding")
	}
}

// TestSuperuserSeedKeepsAChangedPassword is the fault the seeding exists to
// close: a password changed in the admin is not undone by the next start, and
// the line that says so does not print the one in the variables.
func TestSuperuserSeedKeepsAChangedPassword(t *testing.T) {
	app := seededApp(t)
	editSuperuser(t, app, findSuperuser(t, app, seed.Email), func(r *core.Record) {
		r.SetPassword("password-two")
	})
	out := captureLog(t)

	ensureSuperuser(app, seed)

	record := findSuperuser(t, app, seed.Email)
	if !record.ValidatePassword("password-two") {
		t.Error("the password changed in the admin no longer opens the account")
	}
	if record.ValidatePassword(seed.Password) {
		t.Error("the password of the variables opens the account again")
	}
	assertLine(t, out, seed.Password, superuserPasswordEnv, seed.Email, superuserResetEnv)
}

// TestSuperuserSeedKeepsAChangedAddress pins that an address changed from the
// header or the admin does not leave a second superuser behind, and that the
// line names the address nobody has any more, then both ways out: the variable
// to correct, and the flag that creates the account.
func TestSuperuserSeedKeepsAChangedAddress(t *testing.T) {
	app := seededApp(t)
	editSuperuser(t, app, findSuperuser(t, app, seed.Email), func(r *core.Record) {
		r.SetEmail("other@example.com")
	})
	out := captureLog(t)

	ensureSuperuser(app, seed)

	if total := countSuperusers(t, app); total != 1 {
		t.Errorf("%d superusers, want 1", total)
	}
	assertLine(t, out, seed.Password, seed.Email, superuserEmailEnv, superuserResetEnv+"=true")
}

// TestSuperuserSeedTwiceKeepsTheSessions pins that a start with nothing changed
// writes nothing: saving the password again would regenerate the token key, and
// every superuser session would be signed out at each restart.
func TestSuperuserSeedTwiceKeepsTheSessions(t *testing.T) {
	app := seededApp(t)
	before := findSuperuser(t, app, seed.Email).TokenKey()
	out := captureLog(t)

	ensureSuperuser(app, seed)

	if after := findSuperuser(t, app, seed.Email).TokenKey(); after != before {
		t.Error("an unchanged start regenerated the token key")
	}
	if out.Len() != 0 {
		t.Errorf("an unchanged start printed %q, want nothing", out.String())
	}
}

// TestSuperuserWithoutVariablesDoesNothing covers the instance that names no
// account: nothing is created, and nothing is said.
func TestSuperuserWithoutVariablesDoesNothing(t *testing.T) {
	for _, s := range []superuserSettings{
		{},
		{Email: seed.Email},
		{Password: seed.Password},
	} {
		app := freshApp(t)
		out := captureLog(t)

		ensureSuperuser(app, s)

		if total := countSuperusers(t, app); total != 0 {
			t.Errorf("%+v: %d superusers, want none", s, total)
		}
		if out.Len() != 0 {
			t.Errorf("%+v: printed %q, want nothing", s, out.String())
		}
	}
}

// TestSuperuserResetImposesThePassword is the recovery: the flag puts the
// password of the variables back on the account, and says to remove it.
func TestSuperuserResetImposesThePassword(t *testing.T) {
	app := seededApp(t)
	editSuperuser(t, app, findSuperuser(t, app, seed.Email), func(r *core.Record) {
		r.SetPassword("password-lost")
	})
	out := captureLog(t)

	reset := seed
	reset.Reset = true
	ensureSuperuser(app, reset)

	if !findSuperuser(t, app, seed.Email).ValidatePassword(seed.Password) {
		t.Error("the reset password does not open the account")
	}
	assertLine(t, out, seed.Password, superuserResetEnv, seed.Email)
}

// TestSuperuserResetCreatesAnUnknownAddress pins that the recovery succeeds
// even when the address was changed in the admin: it creates a second account,
// which is what the documentation tells the operator to clean up.
func TestSuperuserResetCreatesAnUnknownAddress(t *testing.T) {
	app := seededApp(t)
	editSuperuser(t, app, findSuperuser(t, app, seed.Email), func(r *core.Record) {
		r.SetEmail("other@example.com")
	})
	captureLog(t)

	reset := seed
	reset.Reset = true
	ensureSuperuser(app, reset)

	if !findSuperuser(t, app, seed.Email).ValidatePassword(seed.Password) {
		t.Error("the reset password does not open the account")
	}
	if total := countSuperusers(t, app); total != 2 {
		t.Errorf("%d superusers, want 2", total)
	}
}

// TestSuperuserResetWithAMissingVariable covers the flag posed alone: it
// touches no account, and says it is inoperative.
func TestSuperuserResetWithAMissingVariable(t *testing.T) {
	for _, s := range []superuserSettings{
		{Email: seed.Email, Reset: true},
		{Password: "password-new", Reset: true},
	} {
		app := seededApp(t)
		out := captureLog(t)

		ensureSuperuser(app, s)

		if total := countSuperusers(t, app); total != 1 {
			t.Errorf("%+v: %d superusers, want 1", s, total)
		}
		if !findSuperuser(t, app, seed.Email).ValidatePassword(seed.Password) {
			t.Errorf("%+v: the account was touched", s)
		}
		assertLine(t, out, "password-new", superuserResetEnv)
	}
}

// TestSuperuserSettingsFromEnv covers the reading: the pair taken as it stands,
// and a flag that cannot be read refused by name, which is what stops main.
func TestSuperuserSettingsFromEnv(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		t.Setenv(superuserEmailEnv, seed.Email)
		t.Setenv(superuserPasswordEnv, seed.Password)
		t.Setenv(superuserResetEnv, "true")

		got, err := superuserSettingsFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		want := superuserSettings{Email: seed.Email, Password: seed.Password, Reset: true}
		if got != want {
			t.Errorf("superuserSettingsFromEnv() = %+v, want %+v", got, want)
		}
	})

	t.Run("unreadable", func(t *testing.T) {
		t.Setenv(superuserResetEnv, "treu")

		_, err := superuserSettingsFromEnv()
		if err == nil || !strings.Contains(err.Error(), superuserResetEnv) {
			t.Fatalf("superuserSettingsFromEnv() error = %v, want one naming %s", err, superuserResetEnv)
		}
	})
}

// TestSuperuserSeedFailureIsPrinted pins that a refused save is a line and not
// a stop: the function returns, no account exists, and the installer is left to
// take over.
func TestSuperuserSeedFailureIsPrinted(t *testing.T) {
	app := freshApp(t)
	out := captureLog(t)

	ensureSuperuser(app, superuserSettings{Email: seed.Email, Password: "abc1"})

	if total := countSuperusers(t, app); total != 0 {
		t.Errorf("%d superusers, want none", total)
	}
	assertLine(t, out, "abc1", "seeding failed")
}

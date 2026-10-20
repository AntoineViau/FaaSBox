package main

import (
	"database/sql"
	"errors"
	"log"
	"os"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// The three variables that bring the superuser account into being.
//
// The first two seed it: they create the account on an instance that has none,
// and are left alone once one exists — a password changed in the admin survives
// the next restart. The third is the recovery gesture, the only one a host that
// lets you edit the environment without opening a shell leaves: posed, the two
// are imposed again at every start.
const (
	superuserEmailEnv    = "SUPERUSER_EMAIL"
	superuserPasswordEnv = "SUPERUSER_PASSWORD"
	superuserResetEnv    = "SUPERUSER_RESET"
)

// superuserSettings is what those three variables said, read once at startup.
type superuserSettings struct {
	Email    string
	Password string
	Reset    bool
}

// superuserSettingsFromEnv reads the three. The error comes from the flag alone,
// for the reason demoSettingsFromEnv gives: a flag that imposes a password does
// not fall back on a default.
func superuserSettingsFromEnv() (superuserSettings, error) {
	reset, err := envBool(superuserResetEnv, false)
	if err != nil {
		return superuserSettings{}, err
	}
	return superuserSettings{
		Email:    os.Getenv(superuserEmailEnv),
		Password: os.Getenv(superuserPasswordEnv),
		Reset:    reset,
	}, nil
}

// ensureSuperuser seeds the account, or imposes it again under SUPERUSER_RESET.
//
// It returns nothing on purpose: a failure is printed and the server starts
// anyway, without the account if it had none — PocketBase's installer then takes
// over. A container looping on a password too short would help nobody.
//
// Every line goes to the standard logger, for the reason reportOAuthDisabled
// gives, and none of them ever carries the password. The address is not a
// secret, and it is what the operator needs to read.
func ensureSuperuser(app core.App, s superuserSettings) {
	complete := s.Email != "" && s.Password != ""

	switch {
	case s.Reset && !complete:
		log.Printf("faasbox: %s is set but %s and %s are not both set — no superuser was touched. Remove %s.",
			superuserResetEnv, superuserEmailEnv, superuserPasswordEnv, superuserResetEnv)
	case s.Reset:
		log.Printf("faasbox: %s is set — %s and %s are imposed on superuser %s at every start. Remove %s once you are signed in.",
			superuserResetEnv, superuserEmailEnv, superuserPasswordEnv, s.Email, superuserResetEnv)
		if err := resetSuperuser(app, s); err != nil {
			log.Printf("faasbox: superuser reset failed — %v", err)
		}
	case complete:
		if err := seedSuperuser(app, s); err != nil {
			log.Printf("faasbox: superuser seeding failed — %v", err)
		}
	}
}

// seedSuperuser creates the account when the instance has none, and otherwise
// leaves every account as it stands — saying so when the variables no longer
// describe it, since that line is all an operator who edited SUPERUSER_PASSWORD
// to change it gets to read.
//
// The installer account is left out of the count, as PocketBase itself does: an
// instance started once without the variables carries one, and would otherwise
// never be seeded. Creating the real account deletes it (PocketBase's own hook).
func seedSuperuser(app core.App, s superuserSettings) error {
	total, err := app.CountRecords(core.CollectionNameSuperusers,
		dbx.Not(dbx.HashExp{"email": core.DefaultInstallerEmail}))
	if err != nil {
		return err
	}
	if total == 0 {
		record, err := newSuperuser(app)
		if err != nil {
			return err
		}
		return saveSuperuser(app, record, s)
	}

	record, err := app.FindAuthRecordByEmail(core.CollectionNameSuperusers, s.Email)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		log.Printf("faasbox: no superuser has the address %s — none was created, since superusers already exist. Set %s=true to create it.",
			s.Email, superuserResetEnv)
	case err != nil:
		return err
	case !record.ValidatePassword(s.Password):
		log.Printf("faasbox: %s does not open superuser %s — the account was left as it stands. Set %s=true to impose it.",
			superuserPasswordEnv, s.Email, superuserResetEnv)
	}
	return nil
}

// resetSuperuser is the gesture of PocketBase's `superuser upsert`, found by
// address and created when absent — the recovery has to succeed even when the
// address was changed in the admin. With one difference: only a missing row
// leads to a creation. Any other error is returned and nothing is written, or a
// database incident would attempt a duplicate account.
func resetSuperuser(app core.App, s superuserSettings) error {
	record, err := app.FindAuthRecordByEmail(core.CollectionNameSuperusers, s.Email)
	if errors.Is(err, sql.ErrNoRows) {
		record, err = newSuperuser(app)
	}
	if err != nil {
		return err
	}
	return saveSuperuser(app, record, s)
}

// newSuperuser is an empty record of the superusers collection.
func newSuperuser(app core.App) (*core.Record, error) {
	col, err := app.FindCachedCollectionByNameOrId(core.CollectionNameSuperusers)
	if err != nil {
		return nil, err
	}
	return core.NewRecord(col), nil
}

// saveSuperuser writes the address and the password of the variables onto a
// record, new or found.
func saveSuperuser(app core.App, record *core.Record, s superuserSettings) error {
	record.SetEmail(s.Email)
	record.SetPassword(s.Password)
	return app.Save(record)
}

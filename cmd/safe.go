package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/safe"
)

const sectionSafe = "safe"

// setDefaultSafeConfig sets the defaults of the safe, shared by the server, which
// serves its files, and the safe commands, which convert them.
func setDefaultSafeConfig() {
	s := sectionSafe
	// The upload folder of the historical collector's safe table, shared with it
	// while both run: its files are read and written in its own layout.
	viper.SetDefault(s+".directory", "/oc3/uploads/safe")
	// The master keys, a yaml file of mode 0600 (see safe.LoadKeyring), for
	// instance a key of a sec object. None: the files are stored in plaintext.
	viper.SetDefault(s+".keys_file", "")
	// off, optional or required: see safe.Mode. Optional encrypts as soon as a
	// key is configured; keep it off, or the keys file away, while the historical
	// collector, which reads plaintext only, shares the directory.
	viper.SetDefault(s+".encryption", string(safe.ModeOptional))
	viper.SetDefault(s+".max_size", 16*1024*1024)
	// The mode of the files written, so that the historical collector, if it runs
	// as another user of the group, can still read them.
	viper.SetDefault(s+".file_mode", "0640")
}

// newSafeService builds the safe from the configuration. A configured keys file
// that cannot be read stops the caller, rather than leaving it to store in
// plaintext what the operator asked to encrypt.
func newSafeService() (*safe.Service, error) {
	setDefaultSafeConfig()
	s := sectionSafe
	mode, err := safe.ParseMode(viper.GetString(s + ".encryption"))
	if err != nil {
		return nil, err
	}
	fileMode, err := strconv.ParseUint(viper.GetString(s+".file_mode"), 8, 32)
	if err != nil || fileMode&^0o777 != 0 {
		return nil, fmt.Errorf("%s.file_mode: %q is not an octal mode", s, viper.GetString(s+".file_mode"))
	}
	svc := &safe.Service{
		Store:   safe.Store{Dir: viper.GetString(s + ".directory"), FileMode: osFileMode(fileMode)},
		Mode:    mode,
		MaxSize: viper.GetInt64(s + ".max_size"),
	}
	if path := viper.GetString(s + ".keys_file"); path != "" {
		if svc.Ring, err = safe.LoadKeyring(path); err != nil {
			return nil, err
		}
	}
	if err := svc.Check(); err != nil {
		return nil, err
	}
	slog.Info(fmt.Sprintf("safe: directory %s, encryption %s, files stored from now on %s", svc.Store.Dir, mode,
		map[bool]string{true: "encrypted", false: "in plaintext"}[svc.Encrypts()]))
	return svc, nil
}

func cmdSafe() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "safe",
		Short: "manage the files of the collector safe",
	}
	cmd.AddCommand(cmdSafeEncrypt(), cmdSafeRewrap())
	return cmd
}

// safeBatch is how many rows a conversion reads at a time.
const safeBatch = 100

func cmdSafeEncrypt() *cobra.Command {
	var confirm bool
	cmd := &cobra.Command{
		Use:   "encrypt",
		Short: "encrypt at rest the safe files still stored in plaintext",
		Long: "Encrypt at rest, in place, every safe file version still stored in plaintext, " +
			"with the active master key. The historical collector cannot read the files " +
			"converted: run it once it no longer shares the safe directory. Idempotent, it " +
			"can be stopped and run again.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := setup(sectionSafe); err != nil {
				return err
			}
			if !confirm {
				return errors.New("the historical collector cannot read the files converted: run again with --confirm once it no longer shares the safe directory")
			}
			return safeEach(func(ctx context.Context, svc *safe.Service, odb *cdb.DB) ([]*cdb.SafeFile, func(*cdb.SafeFile) error, error) {
				if svc.Ring.Active() == nil {
					return nil, nil, errors.New("no master key is configured (safe.keys_file)")
				}
				files, err := odb.SafeFilesToConvert(ctx, safeBatch)
				return files, func(f *cdb.SafeFile) error { return svc.Encrypt(ctx, odb, f) }, err
			}, "encrypted")
		},
	}
	cmd.Flags().BoolVar(&confirm, "confirm", false, "confirm that the historical collector no longer reads the safe directory")
	return cmd
}

func cmdSafeRewrap() *cobra.Command {
	return &cobra.Command{
		Use:   "rewrap",
		Short: "wrap the keys of the encrypted safe files and of the secrets with the active master key",
		Long: "After adding a master key and making it the active one, wrap with it the keys " +
			"of the files encrypted with the others, and of the secrets sealed with them in " +
			"the database (form connectors, notification channels); the contents are left " +
			"as they are. The previous keys can be removed from the keys file once it is done.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := setup(sectionSafe); err != nil {
				return err
			}
			secretsErr := rewrapSealedSecrets()
			filesErr := safeEach(func(ctx context.Context, svc *safe.Service, odb *cdb.DB) ([]*cdb.SafeFile, func(*cdb.SafeFile) error, error) {
				active := svc.Ring.Active()
				if active == nil {
					return nil, nil, errors.New("no master key is configured (safe.keys_file)")
				}
				files, err := odb.SafeFilesToRewrap(ctx, active.ID, safeBatch)
				return files, func(f *cdb.SafeFile) error { return svc.Rewrap(ctx, odb, f) }, err
			}, "rewrapped")
			return errors.Join(secretsErr, filesErr)
		},
	}
}

// rewrapSealedSecrets wraps with the active master key the keys of the secrets
// sealed in the database with another one (cdb.SealedSecretTables), a batch
// after the other. A row that fails is reported and skipped; it fails if any
// did.
func rewrapSealedSecrets() error {
	ctx := context.Background()
	svc, err := newSafeService()
	if err != nil {
		return err
	}
	if svc.Ring == nil || svc.Ring.Active() == nil {
		return errors.New("no master key is configured (safe.keys_file)")
	}
	active := svc.Ring.Active()
	db, err := newDatabase(sectionSafe)
	if err != nil {
		return err
	}
	odb := cdb.New(db)
	failed := map[string]bool{}
	var count int
	for {
		refs, err := odb.SealedSecretsToRewrap(ctx, active.ID, safeBatch)
		if err != nil {
			return err
		}
		progress := false
		for _, r := range refs {
			key := r.Table.Table + ":" + strconv.FormatInt(r.ID, 10)
			if failed[key] {
				continue
			}
			keyID, wrapped, err := safe.RewrapSecret(svc.Ring, r.Table.Label(r.ID), r.KeyID, r.WrappedKey)
			if err == nil {
				var ok bool
				if ok, err = odb.SetSealedSecretKey(ctx, r, keyID, wrapped); err == nil && !ok {
					err = errors.New("the row changed during the rewrap")
				}
			}
			if err != nil {
				slog.Error(fmt.Sprintf("secrets of %s %d (%s): %s", r.Table.Kind, r.ID, r.Name, err))
				failed[key] = true
				continue
			}
			count++
			progress = true
		}
		if !progress {
			break
		}
	}
	slog.Info(fmt.Sprintf("safe: %d secret(s) rewrapped, %d failed", count, len(failed)))
	if len(failed) > 0 {
		return fmt.Errorf("%d secret(s) could not be rewrapped", len(failed))
	}
	return nil
}

// safeEach applies an operation to the rows a batch function selects, a batch
// after the other, until none is left or a whole batch failed. A row that fails
// is reported and skipped; the command fails if any did.
func safeEach(batch func(context.Context, *safe.Service, *cdb.DB) ([]*cdb.SafeFile, func(*cdb.SafeFile) error, error), done string) error {
	ctx := context.Background()
	svc, err := newSafeService()
	if err != nil {
		return err
	}
	db, err := newDatabase(sectionSafe)
	if err != nil {
		return err
	}
	odb := cdb.New(db)
	failed := map[int64]bool{}
	var count int
	for {
		files, apply, err := batch(ctx, svc, odb)
		if err != nil {
			return err
		}
		progress := false
		for _, f := range files {
			if failed[f.ID] {
				continue
			}
			if err := apply(f); err != nil {
				slog.Error(fmt.Sprintf("safe file %d (%s): %s", f.ID, f.UUID, err))
				failed[f.ID] = true
				continue
			}
			count++
			progress = true
		}
		if !progress {
			break
		}
	}
	slog.Info(fmt.Sprintf("safe: %d file(s) %s, %d failed", count, done, len(failed)))
	if len(failed) > 0 {
		return fmt.Errorf("%d safe file(s) could not be %s", len(failed), done)
	}
	return nil
}

func osFileMode(m uint64) os.FileMode { return os.FileMode(m) }

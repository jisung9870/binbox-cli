package bb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// Removed fields stay restorable for a week inside a second age file; the
// plaintext store format itself is unchanged.
const secretTrashRetention = 7 * 24 * time.Hour

type secretTrashEntry struct {
	Service   string      `json:"service"`
	Field     string      `json:"field"`
	Value     string      `json:"value"`
	Meta      *secretMeta `json:"meta,omitempty"`
	DeletedAt time.Time   `json:"deleted_at"`
}

type secretTrash struct {
	Entries []secretTrashEntry `json:"entries"`
}

func (a *App) secTrashPath() string {
	store, _ := a.secPaths()
	return filepath.Join(filepath.Dir(store), "secrets.trash.json.age")
}

// readSecretTrash returns live entries and the ciphertext used as the write
// precondition. A missing trash file is an empty trash.
func (a *App) readSecretTrash() (secretTrash, []byte, error) {
	path := a.secTrashPath()
	if _, e := os.Lstat(path); os.IsNotExist(e) {
		return secretTrash{}, nil, nil
	}
	plain, cipher, e := a.decryptSecretFile(path, "secret trash")
	if e != nil {
		return secretTrash{}, nil, e
	}
	var trash secretTrash
	if e := json.Unmarshal(plain, &trash); e != nil {
		return secretTrash{}, nil, fmt.Errorf("parse decrypted secret trash: %w", e)
	}
	return trash, cipher, nil
}

// pruneSecretTrash drops expired entries in memory and reports whether any
// were dropped, so readers can persist the purge.
func (a *App) pruneSecretTrash(trash *secretTrash) bool {
	cutoff := a.now().Add(-secretTrashRetention)
	kept := trash.Entries[:0]
	for _, entry := range trash.Entries {
		if entry.DeletedAt.After(cutoff) {
			kept = append(kept, entry)
		}
	}
	pruned := len(kept) != len(trash.Entries)
	trash.Entries = kept
	return pruned
}

// purgeFromTrash removes every trashed copy of keys so a purge cannot be
// undone with restore.
func (a *App) purgeFromTrash(keys []string) error {
	trash, old, e := a.readSecretTrash()
	if e != nil || old == nil {
		return e
	}
	drop := make(map[string]bool, len(keys))
	for _, key := range keys {
		drop[key] = true
	}
	a.pruneSecretTrash(&trash)
	kept := trash.Entries[:0]
	for _, entry := range trash.Entries {
		if !drop[secretKey(entry.Service, entry.Field)] {
			kept = append(kept, entry)
		}
	}
	trash.Entries = kept
	return a.writeEncryptedJSON(a.secTrashPath(), trash, old, false)
}

// moveSecretsToTrash records entries in the trash before the caller rewrites
// the store, so a failure between the two writes can only duplicate a value.
func (a *App) moveSecretsToTrash(entries []secretTrashEntry) error {
	trash, old, e := a.readSecretTrash()
	if e != nil {
		return e
	}
	a.pruneSecretTrash(&trash)
	trash.Entries = append(trash.Entries, entries...)
	return a.writeEncryptedJSON(a.secTrashPath(), trash, old, false)
}

func (a *App) secTrash(args []string) error {
	args, empty := takeFlag(args, "--empty")
	args, yes := takeFlag(args, "--yes")
	if len(args) != 0 {
		return usage("sec trash", "[--empty [--yes]]")
	}
	trash, old, e := a.readSecretTrash()
	if e != nil {
		return e
	}
	if a.pruneSecretTrash(&trash) && !empty {
		if e := a.writeEncryptedJSON(a.secTrashPath(), trash, old, false); e != nil {
			return fmt.Errorf("purge expired trash entries: %w", e)
		}
	}
	if empty {
		if len(trash.Entries) == 0 && old == nil {
			return nil
		}
		if !yes {
			ok, confirmErr := a.confirmAction(fmt.Sprintf("Permanently delete %d trashed secret field(s)?", len(trash.Entries)))
			if confirmErr != nil {
				return confirmErr
			}
			if !ok {
				return invalid("trash purge cancelled")
			}
		}
		return a.writeEncryptedJSON(a.secTrashPath(), secretTrash{}, old, false)
	}
	sort.SliceStable(trash.Entries, func(i, j int) bool { return trash.Entries[i].DeletedAt.After(trash.Entries[j].DeletedAt) })
	w := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	for _, entry := range trash.Entries {
		left := entry.DeletedAt.Add(secretTrashRetention).Sub(a.now())
		fmt.Fprintf(w, "%s\tdeleted %s\tpurged in %dd\n",
			secretKey(entry.Service, entry.Field),
			entry.DeletedAt.Local().Format("2006-01-02 15:04"),
			int(left.Hours()/24)+1)
	}
	return w.Flush()
}

// secRestore moves the newest trashed copy of a field, or of every field of a
// service, back into the store. Existing fields are never overwritten.
func (a *App) secRestore(args []string) error {
	if len(args) == 0 {
		return a.secTrash(nil)
	}
	if len(args) > 2 || !validSecretName(args[0]) || (len(args) == 2 && !validSecretName(args[1])) {
		return usage("sec restore", "<service[/field]>")
	}
	service, field := args[0], ""
	if len(args) == 2 {
		field = args[1]
	}
	trash, oldTrash, e := a.readSecretTrash()
	if e != nil {
		return e
	}
	a.pruneSecretTrash(&trash)
	// Newest entry wins per field.
	picked := map[string]int{}
	for i, entry := range trash.Entries {
		if entry.Service != service || (field != "" && entry.Field != field) {
			continue
		}
		key := secretKey(entry.Service, entry.Field)
		if j, ok := picked[key]; !ok || entry.DeletedAt.After(trash.Entries[j].DeletedAt) {
			picked[key] = i
		}
	}
	if len(picked) == 0 {
		return invalid("no trashed secret matches " + strings.TrimSuffix(secretKey(service, field), "/"))
	}
	data, oldStore, e := a.readSecretsSnapshot()
	if e != nil {
		return e
	}
	restored := make([]secretTrashEntry, 0, len(picked))
	for key, i := range picked {
		entry := trash.Entries[i]
		if _, exists := data[entry.Service][entry.Field]; exists {
			return invalid("secret field already exists, remove or rename it first: " + key)
		}
		restored = append(restored, entry)
	}
	for _, entry := range restored {
		if data[entry.Service] == nil {
			data[entry.Service] = map[string]string{}
		}
		data[entry.Service][entry.Field] = entry.Value
	}
	if e := a.writeSecrets(data, oldStore); e != nil {
		return e
	}
	remaining := trash.Entries[:0]
	for _, entry := range trash.Entries {
		if _, ok := picked[secretKey(entry.Service, entry.Field)]; !ok {
			remaining = append(remaining, entry)
		}
	}
	trash.Entries = remaining
	if e := a.writeEncryptedJSON(a.secTrashPath(), trash, oldTrash, false); e != nil {
		return fmt.Errorf("restored, but could not update the trash: %w", e)
	}
	if e := a.updateSecretMeta(func(entries map[string]secretMeta) error {
		for _, entry := range restored {
			if entry.Meta != nil {
				entries[secretKey(entry.Service, entry.Field)] = *entry.Meta
			}
		}
		return nil
	}); e != nil {
		return fmt.Errorf("restored, but could not restore metadata: %w", e)
	}
	for _, entry := range restored {
		fmt.Fprintln(a.err, "Restored "+secretKey(entry.Service, entry.Field))
	}
	return nil
}

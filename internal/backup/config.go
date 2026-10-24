package backup

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/s3"
)

// The configuration is <data>/backup.json, mode 0600: it holds the secret key
// and the passphrase, so it is the one file of the data directory the backup
// leaves out (paths.NotBackedUp). A restore is the moment it is typed in
// again.

// DefaultPrefix is where in the bucket a backup goes when nothing else is
// said. It is a folder of its own so that a bucket the mirror of earlier
// builds wrote to keeps those objects apart from the backup's.
const DefaultPrefix = "backup/v1/"

// DefaultInterval and MinInterval bound how often a backup runs.
const (
	DefaultInterval = 10 * time.Minute
	MinInterval     = 5 * time.Minute
)

// defaultRegion is what a configuration without a region signs for: AWS's
// default, which most S3 compatible stores accept when they do not care.
const defaultRegion = "us-east-1"

// Config is backup.json: where the backup goes, how to sign for it, the
// passphrase it is encrypted with, and how often it runs.
type Config struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	PathStyle       bool   `json:"path_style"`
	Passphrase      string `json:"passphrase"`
	// IntervalMinutes is how often a backup runs while anything changes.
	IntervalMinutes int `json:"interval_minutes"`
	// HostID tells this machine's snapshots apart from another's in the same
	// bucket. It is made once per machine and never restored: a restored
	// machine is a new one.
	HostID string `json:"host_id"`
}

// NormalizePrefix makes a prefix the one spelling the object keys are built
// from: no leading slash and exactly one trailing one. An empty prefix is
// DefaultPrefix.
func NormalizePrefix(prefix string) string {
	p := strings.Trim(strings.TrimSpace(prefix), "/")
	if p == "" {
		return DefaultPrefix
	}
	return p + "/"
}

// Normalize trims every field and applies the defaults. It is idempotent.
func (c *Config) Normalize() {
	c.Endpoint = strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	c.Region = strings.TrimSpace(c.Region)
	if c.Region == "" {
		c.Region = defaultRegion
	}
	c.Bucket = strings.TrimSpace(c.Bucket)
	c.Prefix = NormalizePrefix(c.Prefix)
	c.AccessKeyID = strings.TrimSpace(c.AccessKeyID)
	c.SecretAccessKey = strings.TrimSpace(c.SecretAccessKey)
	// The passphrase is not trimmed. A space at its end is unusual but it is
	// the person's, and trimming it would derive a key nobody can reproduce
	// from what they typed.
	if c.IntervalMinutes <= 0 {
		c.IntervalMinutes = int(DefaultInterval / time.Minute)
	}
	if c.IntervalMinutes < int(MinInterval/time.Minute) {
		c.IntervalMinutes = int(MinInterval / time.Minute)
	}
	// A host id is part of every snapshot's object name. One written by
	// hand that is not spelled like one would put snapshots where a listing
	// does not find them - and garbage collection would not count their
	// chunks - so it is dropped, and a new one is made in its place.
	c.HostID = strings.TrimSpace(c.HostID)
	if c.HostID != "" && !validHostID.MatchString(c.HostID) {
		c.HostID = ""
	}
}

// Interval is how often a backup runs.
func (c Config) Interval() time.Duration { return time.Duration(c.IntervalMinutes) * time.Minute }

// Validate says what is missing, in a sentence a form can show.
func (c Config) Validate() error {
	switch {
	case c.Bucket == "":
		return errors.New("the bucket name is missing")
	case c.AccessKeyID == "" || c.SecretAccessKey == "":
		return errors.New("the access key id and the secret access key are both needed")
	case c.Passphrase == "":
		return errors.New("the encryption passphrase is missing")
	}
	_, err := s3.New(c.S3())
	return err
}

// S3 is the client configuration.
func (c Config) S3() s3.Config {
	return s3.Config{
		Endpoint:        c.Endpoint,
		Region:          c.Region,
		Bucket:          c.Bucket,
		PathStyle:       c.PathStyle,
		AccessKeyID:     c.AccessKeyID,
		SecretAccessKey: c.SecretAccessKey,
	}
}

// Place names where the backup is: endpoint, bucket and prefix. The local
// index belongs to one place, and is started again for another.
func (c Config) Place() string {
	endpoint := strings.ToLower(c.Endpoint)
	if endpoint != "" && !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	return endpoint + "|" + c.Bucket + "|" + NormalizePrefix(c.Prefix)
}

// Public is the configuration without its two secrets, for status pages.
type Public struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"access_key_id"`
	PathStyle       bool   `json:"path_style"`
	IntervalMinutes int    `json:"interval_minutes"`
	HostID          string `json:"host_id"`
	// HasSecret and HasPassphrase say that the two secrets are set, without
	// saying what they are: the form shows them as write-only fields.
	HasSecret     bool `json:"has_secret"`
	HasPassphrase bool `json:"has_passphrase"`
}

// Public strips the secret key and the passphrase.
func (c Config) Public() Public {
	return Public{
		Endpoint: c.Endpoint, Region: c.Region, Bucket: c.Bucket, Prefix: c.Prefix,
		AccessKeyID: c.AccessKeyID, PathStyle: c.PathStyle, IntervalMinutes: c.IntervalMinutes,
		HostID: c.HostID, HasSecret: c.SecretAccessKey != "", HasPassphrase: c.Passphrase != "",
	}
}

// LoadConfig reads backup.json. A data directory without one is a nil
// config and no error: backups are off. A file that is there but unreadable
// is an error, so that a broken file is said rather than taken for "off".
func LoadConfig(dataDir string) (*Config, error) {
	file := paths.New(dataDir).BackupConfig()
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", file, err)
	}
	cfg.Normalize()
	return &cfg, nil
}

// SaveConfig writes backup.json, mode 0600, through a temporary file and a
// rename, so a crash half way leaves the old file or the new one and never a
// truncated one. A config without a host id gets one here.
func SaveConfig(dataDir string, cfg Config) error {
	cfg.Normalize()
	if cfg.HostID == "" {
		cfg.HostID = NewHostID()
	}
	blob, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(paths.New(dataDir).BackupConfig(), append(blob, '\n'), 0o600)
}

// writeFileAtomic writes data to file through a temporary file beside it, a
// sync and a rename.
func writeFileAtomic(file string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(file)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

var hostChars = regexp.MustCompile(`[^a-z0-9-]+`)

// NewHostID makes the id of this machine's snapshots: its host name, made
// safe for an object key, and a random part, so that two machines with the
// same name are still two.
func NewHostID() string {
	name, _ := os.Hostname()
	name = strings.Trim(hostChars.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(name) > 32 {
		name = strings.Trim(name[:32], "-")
	}
	if name == "" {
		name = "socrates"
	}
	raw := make([]byte, 3)
	_, _ = rand.Read(raw)
	return name + "-" + hex.EncodeToString(raw)
}

// validHostID is what a host id read from the bucket must look like.
var validHostID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// The mirror of earlier builds was configured by these variables or by
// <data>/s3.json; MigrateConfig carries either over once.
const (
	envEndpoint   = "SOCRATES_S3_ENDPOINT"
	envRegion     = "SOCRATES_S3_REGION"
	envBucket     = "SOCRATES_S3_BUCKET"
	envPrefix     = "SOCRATES_S3_PREFIX"
	envAccessKey  = "SOCRATES_S3_ACCESS_KEY_ID"
	envSecretKey  = "SOCRATES_S3_SECRET_ACCESS_KEY"
	envPathStyle  = "SOCRATES_S3_PATH_STYLE"
	envPassphrase = "SOCRATES_S3_PASSPHRASE"
)

// MigrateConfig carries the bucket mirror of earlier builds over into
// backup.json, once per data directory: from the SOCRATES_S3_* environment
// when SOCRATES_S3_BUCKET is set, else from s3.json - the mirror's own
// precedence, under which a stale s3.json lost to the environment. The
// backup goes into the same bucket with the same credentials and passphrase,
// under DefaultPrefix inside the mirror's prefix, so that the mirror's
// objects are left as they are and the two never meet. It reports whether it
// wrote anything; s3.json itself is left in place.
//
// Whatever it finds - a mirror to carry over, a backup.json already there,
// or nothing - it leaves paths.BackupMigrated behind, and with that marker it
// does nothing: a backup turned off by removing backup.json stays off, and
// an environment set later configures nothing, as the Backup card owns the
// configuration from then on. It is for a data directory that holds an
// installation; on a new machine it would carry the old machine's bucket
// over before it was set up or restored.
func MigrateConfig(dataDir string, getenv func(string) string) (bool, error) {
	layout := paths.New(dataDir)
	marker := layout.Path(paths.BackupMigrated)
	if _, err := os.Stat(marker); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	done := func() error { return markMigrated(dataDir) }
	if _, err := os.Stat(layout.BackupConfig()); err == nil {
		return false, done()
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	var old struct {
		Endpoint        string `json:"endpoint"`
		Region          string `json:"region"`
		Bucket          string `json:"bucket"`
		Prefix          string `json:"prefix"`
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
		PathStyle       bool   `json:"path_style"`
		Passphrase      string `json:"passphrase"`
	}
	if strings.TrimSpace(getenv(envBucket)) != "" {
		old.Endpoint, old.Region, old.Bucket = getenv(envEndpoint), getenv(envRegion), getenv(envBucket)
		old.Prefix, old.AccessKeyID, old.SecretAccessKey = getenv(envPrefix), getenv(envAccessKey), getenv(envSecretKey)
		old.Passphrase = getenv(envPassphrase)
		old.PathStyle = true
		if v := strings.TrimSpace(getenv(envPathStyle)); v != "" {
			if b, err := strconv.ParseBool(v); err == nil {
				old.PathStyle = b
			}
		}
	} else {
		raw, err := os.ReadFile(layout.Path(paths.LegacyS3Config))
		switch {
		case errors.Is(err, os.ErrNotExist):
			return false, done()
		case err != nil:
			return false, err
		}
		if err := json.Unmarshal(raw, &old); err != nil {
			return false, fmt.Errorf("%s is not valid JSON: %w", paths.LegacyS3Config, err)
		}
	}
	if strings.TrimSpace(old.Bucket) == "" {
		return false, done()
	}
	inside := strings.Trim(strings.TrimSpace(old.Prefix), "/")
	if inside != "" {
		inside += "/"
	}
	cfg := Config{
		Endpoint: old.Endpoint, Region: old.Region, Bucket: old.Bucket,
		Prefix:      inside + DefaultPrefix,
		AccessKeyID: old.AccessKeyID, SecretAccessKey: old.SecretAccessKey,
		PathStyle: old.PathStyle, Passphrase: old.Passphrase,
	}
	if err := SaveConfig(dataDir, cfg); err != nil {
		return false, err
	}
	return true, done()
}

// markMigrated leaves paths.BackupMigrated behind: the mirror's
// configuration is not carried over into backup.json again.
func markMigrated(dataDir string) error {
	return writeFileAtomic(paths.New(dataDir).Path(paths.BackupMigrated),
		[]byte("The bucket mirror's configuration has been carried over into backup.json.\n"), 0o600)
}

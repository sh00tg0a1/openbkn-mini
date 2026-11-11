// Package safe prepares what the platform's own sign-in and permissions need
// when mini runs them (MINI_SAFE=on) in place of the stand-ins: bkn-safe's
// settings and database, an ORY Hydra process, and the OAuth clients.
package safe

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"regexp"

	_ "github.com/go-sql-driver/mysql"
	"gopkg.in/yaml.v3"
)

// Config says where the parts listen and how the browser reaches mini.
type Config struct {
	PublicURL       string // the gateway, as the browser sees it
	SafeAddr        string // bkn-safe
	HydraPublicAddr string
	HydraAdminAddr  string
	HydraBin        string // the hydra program
	HydraDir        string // Hydra's database and secret
	Database        Database
	Upstreams       Upstreams
}

// Database is the server bkn-safe keeps its own database on: the one the other
// services use.
type Database struct {
	Host     string `yaml:"host"`
	Port     string `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
}

// DatabaseFrom reads the database server from a platform service's config
// file (depServices.rds), so mini names it in one place.
func DatabaseFrom(path string) (Database, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Database{}, err
	}
	var file struct {
		DepServices struct {
			RDS Database `yaml:"rds"`
		} `yaml:"depServices"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		return Database{}, fmt.Errorf("%s: %w", path, err)
	}
	db := file.DepServices.RDS
	if db.Host == "" || db.Port == "" || db.User == "" {
		return Database{}, fmt.Errorf("%s: depServices.rds needs host, port and user", path)
	}
	return db, nil
}

// Upstreams are the services bkn-safe asks about the resources it guards.
type Upstreams struct {
	BKNBackend, ExecutionFactory, VegaBackend, OntologyQuery string // host:port
}

// Env returns bkn-safe's settings for mini. bkn-safe reads them from the
// process environment; a variable already set there wins.
func Env(cfg Config) map[string]string {
	redirects, _ := json.Marshal([]string{StudioRedirectURI(cfg.PublicURL)})
	return map[string]string{
		"SAFE_HTTP_ADDR":     cfg.SafeAddr,
		"SAFE_SEED_ON_START": "true",
		"SAFE_DB_TYPE":       "MySQL",
		"SAFE_DB_HOST":       cfg.Database.Host,
		"SAFE_DB_PORT":       cfg.Database.Port,
		"SAFE_DB_USER":       cfg.Database.User,
		"SAFE_DB_PASSWORD":   cfg.Database.Password,
		"SAFE_DB_NAME":       "safe",

		"SAFE_HYDRA_ADMIN_URL":                     "http://" + cfg.HydraAdminAddr,
		"SAFE_HYDRA_PUBLIC_URL":                    "http://" + cfg.HydraPublicAddr,
		"SAFE_HYDRA_BROWSER_PUBLIC_URL":            cfg.PublicURL,
		"SAFE_OAUTH_STUDIO_BASELINE_REDIRECT_URIS": string(redirects),
		// MCP clients register themselves and sign the user in.
		"SAFE_OAUTH_DYNAMIC_REGISTRATION_ENABLED": "true",

		"SAFE_BKN_BACKEND_BASE_URL":       "http://" + cfg.Upstreams.BKNBackend,
		"SAFE_EXECUTION_FACTORY_BASE_URL": "http://" + cfg.Upstreams.ExecutionFactory,
		"SAFE_VEGA_BACKEND_BASE_URL":      "http://" + cfg.Upstreams.VegaBackend,
		"SAFE_ONTOLOGY_QUERY_BASE_URL":    "http://" + cfg.Upstreams.OntologyQuery,

		// mini does not call the licence server unless told to.
		"SAFE_LICENSE_SERVER_URL": "",
		"GIN_MODE":                "release",
	}
}

// Setenv applies Env to the process, keeping variables that are already set.
func Setenv(cfg Config) {
	for k, v := range Env(cfg) {
		if _, set := os.LookupEnv(k); !set {
			_ = os.Setenv(k, v)
		}
	}
}

var plainName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// EnsureDatabase creates bkn-safe's database if it is missing. bkn-safe makes
// its own tables but expects the database to exist. It reads the SAFE_DB_*
// variables, so call it after Setenv.
func EnsureDatabase() error {
	name := os.Getenv("SAFE_DB_NAME")
	if !plainName.MatchString(name) {
		return fmt.Errorf("SAFE_DB_NAME %q: use letters, digits and underscores", name)
	}
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/", os.Getenv("SAFE_DB_USER"), os.Getenv("SAFE_DB_PASSWORD"),
		os.Getenv("SAFE_DB_HOST"), os.Getenv("SAFE_DB_PORT"))
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec("CREATE DATABASE IF NOT EXISTS `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	if err != nil {
		return fmt.Errorf("create database %s: %w", name, err)
	}
	return nil
}

// StudioRedirectURI is where Hydra sends the browser back after sign-in.
func StudioRedirectURI(publicURL string) string { return publicURL + "/studio/callback" }

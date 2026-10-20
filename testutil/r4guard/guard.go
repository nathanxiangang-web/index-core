// Package r4guard protects destructive R4 TEST fixtures, never production code.
package r4guard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const Database = "indexcore_r4_125_test"

type Attestation struct {
	Host      string `json:"host"`
	Container string `json:"container"`
	Nonce     string `json:"nonce"`
	System    string `json:"system"`
	OID       uint32 `json:"oid"`
}

var hex64 = regexp.MustCompile(`^[a-f0-9]{64}$`)
var hostPattern = regexp.MustCompile(`^indexcore-r4-125-[a-f0-9]{12}-db$`)
var digits = regexp.MustCompile(`^[0-9]+$`)

// Open rejects absent opt-in, ambiguous DSNs and unproven instances BEFORE any
// reset. The orchestrator binds a read-only attestation after Docker ownership,
// network, mount and resource checks; SQL also verifies server nonce, physical
// system_identifier and an out-of-public-schema marker tied to this DB OID.
func Open(ctx context.Context) (*pgxpool.Pool, error) {
	refused := errors.New("R4 safety guard refused unverified disposable instance")
	if os.Getenv("INDEXCORE_R4_OPT_IN") != "isolated-issue-125" {
		return nil, refused
	}
	file := os.Getenv("INDEXCORE_R4_ATTESTATION")
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 4096 {
		return nil, refused
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, refused
	}
	var a Attestation
	if json.Unmarshal(data, &a) != nil || !hostPattern.MatchString(a.Host) || !hex64.MatchString(a.Container) || !hex64.MatchString(a.Nonce) || !digits.MatchString(a.System) || a.OID == 0 {
		return nil, refused
	}
	dsn := os.Getenv("INDEXCORE_TEST_DATABASE_URL")
	if dsn == "" {
		return nil, refused
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || cfg.ConnConfig.Database != Database || cfg.ConnConfig.Host != a.Host || cfg.ConnConfig.Port != 5432 || cfg.ConnConfig.User != "r4" || len(cfg.ConnConfig.Fallbacks) > 0 || cfg.ConnConfig.TLSConfig != nil {
		return nil, refused
	}
	cfg.MaxConns = 4
	cfg.MinConns = 0
	// Test migrations intentionally create unqualified tables in public.
	// With pg_catalog omitted it is implicitly searched first for built-ins,
	// while CREATE targets public rather than the protected catalog schema.
	cfg.ConnConfig.RuntimeParams["search_path"] = "public"
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, refused
	}
	var ok bool
	err = pool.QueryRow(ctx, `SELECT current_database()=$1 AND current_setting('server_version_num')::int BETWEEN 180000 AND 189999
 AND current_setting('indexcore.r4_nonce',true)=$2
 AND (SELECT system_identifier::text FROM pg_catalog.pg_control_system())=$3
 AND (SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database())=$4::oid
 AND EXISTS (SELECT 1 FROM r4_guard.instance WHERE nonce=$2 AND container_id=$5 AND system_id=$3 AND db_oid=$4::oid)`, Database, a.Nonce, a.System, a.OID, a.Container).Scan(&ok)
	if err != nil || !ok {
		pool.Close()
		return nil, refused
	}
	return pool, nil
}

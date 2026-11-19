// seedprobe seeds probe data for app_restore_smoke.sh: generates a one-shot keyring (written to
// SMOKE_KEYRING), creates a scratch table in SMOKE_DB storing two AEAD envelope families
// (matching production parameters: identifier mapping and raw payload AAD shapes), and records
// the schema_migrations version anchor to the JSON file indicated by SMOKE_SEED. Zero business
// table dependencies; envelopes use the production crypto/privacy packages.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"database/sql"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

const deploymentID = "restore-smoke-probe"

func main() {
	dsn := os.Getenv("SMOKE_DSN")
	keyringPath := os.Getenv("SMOKE_KEYRING")
	seedPath := os.Getenv("SMOKE_SEED")
	if dsn == "" || keyringPath == "" || seedPath == "" {
		fmt.Fprintln(os.Stderr, "SMOKE_DSN/SMOKE_KEYRING/SMOKE_SEED required")
		os.Exit(2)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	doc := map[string]any{
		"contract_version": 1, "deployment_id": deploymentID,
		"keys": []map[string]any{{
			"key_id": "smoke-1", "purpose": "payload", "status": "encrypt_decrypt",
			"created_at": "2026-10-29T00:00:00Z",
			"key_base64": base64.StdEncoding.EncodeToString(key),
		}},
	}
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(keyringPath, raw, 0o400); err != nil {
		panic(err)
	}
	if err := os.Chmod(keyringPath, 0o400); err != nil {
		panic(err)
	}
	ring, err := crypto.LoadKeyring(keyringPath)
	if err != nil {
		panic(err)
	}
	c, err := crypto.NewCrypter(ring)
	if err != nil {
		panic(err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		panic(err)
	}
	if err := db.Ping(); err != nil {
		panic(err)
	}
	defer db.Close()

	// scratch table: envelope carrier (touches no business table FKs; restore is carried by pg_dump).
	if _, err := db.Exec(`create table if not exists restore_smoke_probe (
		id text primary key, kind text not null,
		ciphertext bytea not null, nonce bytea not null,
		key_id text not null, algorithm text not null)`); err != nil {
		panic(err)
	}
	// Envelope 1: identifier mapping (production AAD: MappingTable#run#1/MappingColumn).
	pairs := []privacy.MappingPair{
		{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
	}
	menv, err := privacy.SealMapping(c, privacy.EnvelopeBinding{ReviewRunID: "smoke-run", StatementIdx: 1}, pairs)
	if err != nil {
		panic(err)
	}
	// Envelope 2: raw payload shape (PurposePayload + business table AAD).
	renv, err := c.Seal(crypto.PurposePayload, []byte(`{"sql":"select 1","kind":"raw-probe"}`), crypto.AAD{
		DeploymentID: c.DeploymentID(), Table: "evidence_raw_payloads",
		PrimaryKey: "smoke-fact", Column: "raw_payload_ciphertext",
		Purpose: crypto.PurposePayload,
	})
	if err != nil {
		panic(err)
	}
	insert := func(id, kind string, env crypto.Envelope) {
		if _, err := db.Exec(`insert into restore_smoke_probe
			(id, kind, ciphertext, nonce, key_id, algorithm) values ($1,$2,$3,$4,$5,$6)`,
			id, kind, env.Ciphertext, env.Nonce, env.KeyID, "aes-256-gcm"); err != nil {
			panic(err)
		}
	}
	insert("mapping-1", "identifier_mapping", menv)
	insert("raw-1", "raw_payload", renv)

	var tables int
	var structure string
	var agg sql.NullString
	if err := db.QueryRow(`select
		(select count(*) from information_schema.tables where table_schema='public'),
		(select string_agg(t || ':' || c, ',' order by t, c) from (
			select table_name t, string_agg(column_name, ',' order by column_name) c
			from information_schema.columns
			where table_schema='public' group by table_name) s)`).
		Scan(&tables, &agg); err != nil {
		panic(err)
	}
	structure = fmt.Sprintf("%x", sha256.Sum256([]byte(agg.String)))
	out, _ := json.Marshal(map[string]any{
		"keyring_path": keyringPath, "deployment_id": deploymentID,
		"schema_table_count": tables, "schema_structure_sha256": structure,
		"envelopes": []string{"mapping-1", "raw-1"},
	})
	if err := os.WriteFile(seedPath, out, 0o400); err != nil {
		panic(err)
	}
	fmt.Printf("seeded: keyring=%s envelopes=2 tables=%d\n", keyringPath, tables)
}

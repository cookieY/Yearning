// restoreprobe executes app_restore_smoke assertions on the restored database: loads the matching
// keyring, opens both envelope families (AAD verification + plaintext matches seed), and verifies
// the schema_migrations version anchor has not drifted. Exits non-zero on any failure.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"

	"database/sql"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

func main() {
	dsn := os.Getenv("SMOKE_DSN")
	seedPath := os.Getenv("SMOKE_SEED")
	if dsn == "" || seedPath == "" {
		fmt.Fprintln(os.Stderr, "SMOKE_DSN/SMOKE_SEED required")
		os.Exit(2)
	}
	var seed struct {
		KeyringPath        string   `json:"keyring_path"`
		DeploymentID       string   `json:"deployment_id"`
		SchemaTableCount   int      `json:"schema_table_count"`
		SchemaStructureSHA string   `json:"schema_structure_sha256"`
		Envelopes          []string `json:"envelopes"`
	}
	raw, err := os.ReadFile(seedPath)
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(raw, &seed); err != nil {
		panic(err)
	}
	ring, err := crypto.LoadKeyring(seed.KeyringPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL keyring load:", err)
		os.Exit(1)
	}
	c, err := crypto.NewCrypter(ring)
	if err != nil {
		panic(err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		panic(err)
	}

	// Assertion 1: identifier mapping envelope opens and matches expected content.
	var menv crypto.Envelope
	if err := db.QueryRow(`select ciphertext, nonce, key_id, algorithm
		from restore_smoke_probe where id='mapping-1'`).
		Scan(&menv.Ciphertext, &menv.Nonce, &menv.KeyID, &menv.Algorithm); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL mapping row:", err)
		os.Exit(1)
	}
	pairs, err := privacy.OpenMapping(c, privacy.EnvelopeBinding{
		ReviewRunID: "smoke-run", StatementIdx: 1}, menv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL mapping open:", err)
		os.Exit(1)
	}
	if len(pairs) != 1 || pairs[0].Original != "orders" || pairs[0].Token != "table_1" {
		fmt.Fprintln(os.Stderr, "FAIL mapping content:", pairs)
		os.Exit(1)
	}
	fmt.Println("assert ok: identifier mapping envelope opens with paired keyring")

	// Assertion 2: raw payload envelope opens and plaintext matches.
	var renv crypto.Envelope
	if err := db.QueryRow(`select ciphertext, nonce, key_id, algorithm
		from restore_smoke_probe where id='raw-1'`).
		Scan(&renv.Ciphertext, &renv.Nonce, &renv.KeyID, &renv.Algorithm); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL raw row:", err)
		os.Exit(1)
	}
	plain, err := c.Open(crypto.PurposePayload, renv, crypto.AAD{
		DeploymentID: c.DeploymentID(), Table: "evidence_raw_payloads",
		PrimaryKey: "smoke-fact", Column: "raw_payload_ciphertext",
		Purpose: crypto.PurposePayload,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL raw open:", err)
		os.Exit(1)
	}
	if string(plain) != `{"sql":"select 1","kind":"raw-probe"}` {
		fmt.Fprintln(os.Stderr, "FAIL raw content:", string(plain))
		os.Exit(1)
	}
	fmt.Println("assert ok: raw payload envelope opens with paired keyring")

	// Assertion 3: schema version anchor has not drifted (migrations neither replayed nor missing).
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
	if tables != seed.SchemaTableCount || structure != seed.SchemaStructureSHA {
		fmt.Fprintf(os.Stderr, "FAIL schema anchor: got (%d,%s) want (%d,%s)\n",
			tables, structure[:12], seed.SchemaTableCount, seed.SchemaStructureSHA[:12])
		os.Exit(1)
	}
	fmt.Printf("assert ok: schema anchor stable (tables=%d structure=%s)\n", tables, structure)
}

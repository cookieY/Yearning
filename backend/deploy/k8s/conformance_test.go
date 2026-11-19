// conformance_test.go static conformance suite for K8s manifests (K001 acceptance gate):
// statically assertable items from contract conformance_tests — Secret class isolation, pod security,
// probes, PDB, default-deny NetworkPolicy, singleton Job, and topology spread.
// (Runtime items: pod kill/affinity/claim contention are covered by the in-cluster suite.)
package k8s

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// doc represents a single YAML document (split on ---).
type doc struct {
	Kind   string
	Name   string
	Body   string
	Labels map[string]string
}

// loadManifests reads all YAML files under deploy/k8s and splits documents on ---.
func loadManifests(t *testing.T) []doc {
	if dir := os.Getenv("RELEASE_MANIFEST_DIR"); dir != "" {
		return loadManifestsFrom(t, dir)
	}
	return loadManifestsFrom(t, ".")
}

// manifestFile matches the extension set of kubectl apply -f <dir> (H1):
// .yaml/.yml/.json are all loaded; reading only .yaml would leave unreviewed files in rendered directories.
func manifestFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") ||
		strings.HasSuffix(lower, ".json")
}

// loadManifestsFrom reads the specified directory (allowing release-rendered artifacts to be
// verified via RELEASE_MANIFEST_DIR without modifying tracked source manifests).
func loadManifestsFrom(t *testing.T, dir string) []doc {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read manifests dir: %v", err)
	}
	var docs []doc
	for _, e := range entries {
		if e.IsDir() || !manifestFile(e.Name()) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range strings.Split(string(raw), "\n---") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			// Syntax gate: every manifest document must decode cleanly with the YAML parser (including
			// duplicate key detection); regex assertions run only on valid YAML.
			var node map[string]any
			if err := yaml.Unmarshal([]byte(part), &node); err != nil {
				t.Fatalf("%s: invalid YAML: %v", e.Name(), err)
			}
			d := doc{Body: part, Labels: map[string]string{}}
			if m := regexp.MustCompile(`(?m)^kind: (\w+)`).FindStringSubmatch(part); m != nil {
				d.Kind = m[1]
			}
			if m := regexp.MustCompile(`(?m)^  name: ([\w-]+)`).FindStringSubmatch(part); m != nil {
				d.Name = m[1]
			}
			for _, lm := range regexp.MustCompile(`([a-zA-Z0-9./-]+): ([\w./-]+)`).FindAllStringSubmatch(part, -1) {
				if strings.HasPrefix(lm[1], "app.kubernetes.io/") {
					d.Labels[lm[1]] = lm[2]
				}
			}
			docs = append(docs, d)
		}
	}
	return docs
}

// TestLoadManifestsIncludesKubectlExtensions H1 regression: kubectl apply -f <dir>
// loads .yaml/.yml/.json, so the loader must not inspect only .yaml.
func TestLoadManifestsIncludesKubectlExtensions(t *testing.T) {
	dir := t.TempDir()
	manifest := "apiVersion: v1\nkind: Pod\nmetadata:\n  name: extra\nspec:\n  containers:\n  - name: x\n    image: evil.example/x@sha256:" + strings.Repeat("a", 64) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "extra.yml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	jsonManifest := `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"extra-json"},"spec":{"containers":[{"name":"x","image":"evil.example/x@sha256:` + strings.Repeat("b", 64) + `"}]}}`
	if err := os.WriteFile(filepath.Join(dir, "extra.json"), []byte(jsonManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	docs := loadManifestsFrom(t, dir)
	images := 0
	for _, d := range docs {
		images += len(yamlImages(d.Body))
	}
	if images != 2 {
		t.Fatalf("expected 2 images from .yml+.json, got %d (docs=%d)", images, len(docs))
	}
}

// TestYamlImagesNonStringIsAmbiguous H2 companion: non-string image values must not
// be silently skipped, otherwise null/list/binary forms would slip between the two gates.
func TestYamlImagesNonStringIsAmbiguous(t *testing.T) {
	refs := yamlImages("spec:\n  containers:\n  - image: 123\n")
	if len(refs) != 1 || refs[0] != "\x00ambiguous-image" {
		t.Fatalf("non-string image must be ambiguous, got %#v", refs)
	}
}

func findKind(docs []doc, kind string) []doc {
	var out []doc
	for _, d := range docs {
		if d.Kind == kind {
			out = append(out, d)
		}
	}
	return out
}

func findByName(docs []doc, kind, name string) *doc {
	for i := range docs {
		if docs[i].Kind == kind && strings.Contains(docs[i].Name, name) {
			return &docs[i]
		}
	}
	return nil
}

// TestControlPlaneShape K001 core shape: >= 2 replicas, RollingUpdate
// maxUnavailable=0, PDB minAvailable=1, topology spread, dual ports.
func TestControlPlaneShape(t *testing.T) {
	docs := loadManifests(t)
	dep := findByName(docs, "Deployment", "control-plane")
	if dep == nil {
		t.Fatal("control-plane Deployment missing")
	}
	replicas := -1
	if m := regexp.MustCompile(`(?m)^  replicas: (\d+)`).FindStringSubmatch(dep.Body); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			replicas = n
		}
	}
	if replicas < 2 {
		t.Errorf("replicas must be >= 2, got %d", replicas)
	}
	if !strings.Contains(dep.Body, "maxUnavailable: 0") || !strings.Contains(dep.Body, "maxSurge: 1") {
		t.Error("RollingUpdate maxUnavailable=0/maxSurge=1 missing")
	}
	if !strings.Contains(dep.Body, "topologySpreadConstraints") {
		t.Error("topology spread missing")
	}
	if !strings.Contains(dep.Body, "containerPort: 8080") {
		t.Error("port 8080 missing")
	}
	// Observability descoped (RCP-20260829-OBSERVABILITY-DESCOPE): internal metrics
	// ports/Services must not reappear.
	if strings.Contains(dep.Body, "containerPort: 9090") {
		t.Error("metrics port 9090 must not exist after descope")
	}
	pdb := findByName(docs, "PodDisruptionBudget", "control-plane")
	if pdb == nil || !strings.Contains(pdb.Body, "minAvailable: 1") {
		t.Fatal("PDB minAvailable=1 missing")
	}
	if findByName(docs, "Service", "yearning-metrics") != nil {
		t.Error("metrics Service must not exist after observability descope")
	}
	// probes: liveness process-only + readiness + startup.
	for _, probe := range []string{"livenessProbe:", "readinessProbe:", "startupProbe:"} {
		if !strings.Contains(dep.Body, probe) {
			t.Errorf("probe %s missing", probe)
		}
	}
	if !strings.Contains(dep.Body, "terminationGracePeriodSeconds: 300") {
		t.Error("termination grace period 300 missing")
	}
}

// TestPodSecurityContract static assertions for all contract pod_security items.
func TestPodSecurityContract(t *testing.T) {
	docs := loadManifests(t)
	for _, dep := range findKind(docs, "Deployment") {
		checkPodSecurity(t, dep.Name, dep.Body)
	}
	for _, job := range findKind(docs, "Job") {
		checkPodSecurity(t, job.Name, job.Body)
	}
}

func checkPodSecurity(t *testing.T, name, body string) {
	t.Helper()
	for _, frag := range []string{
		"runAsNonRoot: true", "runAsUser: 10001", "allowPrivilegeEscalation: false",
		"readOnlyRootFilesystem: true", "seccompProfile:", "RuntimeDefault",
		"automountServiceAccountToken: false",
	} {
		if !strings.Contains(body, frag) {
			t.Errorf("%s: pod security %s missing", name, frag)
		}
	}
	if !strings.Contains(body, "drop: [\"ALL\"]") && !strings.Contains(body, "drop:\n              - ALL") {
		t.Errorf("%s: capabilities drop ALL missing", name)
	}
	if strings.Contains(body, "hostNetwork: true") || strings.Contains(body, "privileged: true") {
		t.Errorf("%s: forbidden host/privileged", name)
	}
}

// TestSecretClassSeparation K001 Secret class separation: workloads mount only their allowed classes,
// forbidden classes never appear, and the schema-migration Job never mounts the keyring.
func TestSecretClassSeparation(t *testing.T) {
	docs := loadManifests(t)
	cp := findByName(docs, "Deployment", "control-plane")
	if cp == nil {
		t.Fatal("control-plane missing")
	}
	for _, secret := range []string{"yearning-payload-keyring", "yearning-metadata-database-app"} {
		if !strings.Contains(cp.Body, "secretName: "+secret) {
			t.Errorf("control-plane missing secret %s", secret)
		}
	}
	for _, forbidden := range []string{"schema-owner", "migrator", "workbench", "legacy-source"} {
		if strings.Contains(cp.Body, forbidden) {
			t.Errorf("control-plane mounts forbidden secret class: %s", forbidden)
		}
	}
	mr := findByName(docs, "Deployment", "migration-review")
	if mr == nil {
		t.Fatal("migration-review missing")
	}
	for _, forbidden := range []string{"database-app", "schema-owner", "datasource-credentials", "legacy-source"} {
		if strings.Contains(mr.Body, forbidden) {
			t.Errorf("migration-review mounts forbidden secret class: %s", forbidden)
		}
	}
	job := findByName(docs, "Job", "schema-migration")
	if job == nil {
		t.Fatal("schema-migration Job missing")
	}
	if strings.Contains(job.Body, "keyring") {
		t.Error("schema-migration Job must not mount payload keyring")
	}
}

// TestSchemaMigrationJobSingleton asserts backoffLimit=0/parallelism=1/completions=1.
func TestSchemaMigrationJobSingleton(t *testing.T) {
	docs := loadManifests(t)
	job := findByName(docs, "Job", "schema-migration")
	if job == nil {
		t.Fatal("schema-migration Job missing")
	}
	for _, frag := range []string{"parallelism: 1", "completions: 1", "backoffLimit: 0", "restartPolicy: Never"} {
		if !strings.Contains(job.Body, frag) {
			t.Errorf("Job %s missing", frag)
		}
	}
}

// TestNetworkPolicyDefaultDeny asserts default deny, allowed surfaces, and cloud metadata deny rules.
func TestNetworkPolicyDefaultDeny(t *testing.T) {
	docs := loadManifests(t)
	deny := findByName(docs, "NetworkPolicy", "default-deny")
	if deny == nil {
		t.Fatal("default deny NetworkPolicy missing")
	}
	if !strings.Contains(deny.Body, `policyTypes: ["Ingress", "Egress"]`) {
		t.Error("default deny must cover both directions")
	}
	// Default-deny document has empty ingress/egress lists.
	if !strings.Contains(deny.Body, "ingress: []") || !strings.Contains(deny.Body, "egress: []") {
		t.Error("default deny lists must be empty")
	}
	cp := findByName(docs, "NetworkPolicy", "control-plane-allowed")
	if cp == nil {
		t.Fatal("control-plane allowed policy missing")
	}
	// Contract control_plane_egress: external egress must use configurable CIDRs (fail-closed
	// TEST-NET placeholder), forbidding 0.0.0.0/0 default open; SMTP egress port must be present.
	if strings.Contains(cp.Body, "cidr: 0.0.0.0/0") {
		t.Error("control-plane egress must not ship 0.0.0.0/0 default-open rule")
	}
	for _, cidr := range []string{"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24"} {
		if !strings.Contains(cp.Body, "cidr: "+cidr) {
			t.Errorf("fail-closed placeholder CIDR %s missing", cidr)
		}
	}
	for _, port := range []string{"- port: 25", "- port: 465", "- port: 587"} {
		if !strings.Contains(cp.Body, port) {
			t.Errorf("notification SMTP egress port %s missing", strings.TrimPrefix(port, "- "))
		}
	}
	for _, policy := range findKind(docs, "NetworkPolicy") {
		if strings.Contains(policy.Body, "cidr: 0.0.0.0/0") {
			t.Errorf("%s: default-open egress forbidden", policy.Name)
		}
	}
}

// TestRequiredLabels asserts all contract observability.required_labels are present.
func TestRequiredLabels(t *testing.T) {
	docs := loadManifests(t)
	for _, dep := range findKind(docs, "Deployment") {
		for _, l := range []string{"app.kubernetes.io/name", "app.kubernetes.io/instance",
			"app.kubernetes.io/version", "app.kubernetes.io/component"} {
			if _, ok := dep.Labels[l]; !ok {
				t.Errorf("%s: required label %s missing", dep.Name, l)
			}
		}
	}
}

// TestControlPlaneRuntimeContract SEC-0019/SEC-0018: control-plane is runnable directly from repo
// manifests — serve subcommand, environment=production, keyring path env, and database leaf values
// sourced from yearning-metadata-database-app Secret (secretKeyRef, key names rendered by Release
// workflow; sslmode rendered as verify-full).
func TestControlPlaneRuntimeContract(t *testing.T) {
	docs := loadManifests(t)
	cp := findByName(docs, "Deployment", "control-plane")
	if cp == nil {
		t.Fatal("control-plane Deployment missing")
	}
	if !strings.Contains(cp.Body, `args: ["serve"]`) {
		t.Error(`control-plane args ["serve"] missing (entrypoint /yearning requires subcommand)`)
	}
	for _, frag := range []string{
		"- name: YEARNING_ENVIRONMENT", "value: production",
		"- name: YEARNING_KEY_PROVIDER_PATH",
		"value: /var/run/secrets/yearning/keyring/keyring.json",
	} {
		if !strings.Contains(cp.Body, frag) {
			t.Errorf("control-plane env %s missing", frag)
		}
	}
	for _, leaf := range []string{"HOST", "PORT", "USER", "PASSWORD", "DBNAME", "SSLMODE", "SSLROOTCERT"} {
		if !strings.Contains(cp.Body, "- name: YEARNING_DATABASE_"+leaf) {
			t.Errorf("control-plane env YEARNING_DATABASE_%s missing", leaf)
		}
	}
	// Seven database leaf values (metadata-database-app, including optional private CA path)
	// + dedicated retention DSN (yearning-metadata-database-retention,
	// fail-closed RUNTIME domain wiring) must all use secretKeyRef.
	if got := strings.Count(cp.Body, "secretKeyRef:"); got != 8 {
		t.Errorf("control-plane must source 7 database leaves + 1 retention DSN via secretKeyRef, got %d", got)
	}
	for _, frag := range []string{
		"secretKeyRef:", "name: yearning-metadata-database-app",
		"key: host", "key: port", "key: user", "key: password", "key: dbname", "key: sslmode",
		"key: sslrootcert",
	} {
		if !strings.Contains(cp.Body, frag) {
			t.Errorf("control-plane secretKeyRef fragment %s missing", frag)
		}
	}
	// Dedicated retention connection (yearning_retention role; missing in production fails closed
	// during config load): env must be present and reference the dedicated Secret.
	for _, frag := range []string{
		"- name: YEARNING_RETENTION_DATABASE_DSN",
		"name: yearning-metadata-database-retention",
		"key: dsn",
	} {
		if !strings.Contains(cp.Body, frag) {
			t.Errorf("control-plane retention DSN fragment %s missing", frag)
		}
	}
	// SEC-0009 follow-up review requirement: addrguard dev bypass env is forbidden in production manifests
	// (checked against effective config lines after stripYAMLComments; mentions in comments are unrestricted).
	if strings.Contains(stripYAMLComments(cp.Body), "YEARNING_ALLOW_LOOPBACK_TARGETS") {
		t.Error("control-plane must not carry YEARNING_ALLOW_LOOPBACK_TARGETS (addrguard dev bypass)")
	}
}

// TestSecretVolumeHardening SEC-0017: all Secret volumes across the three workloads must set
// defaultMode 0400 (contract file_mode_maximum) and pod securityContext fsGroup 10001
// (kubelet delivers read-only Secret volumes with 0440 mask to runAsUser 10001, and keyring load
// validation accepts 0400/0440 — both settings must appear together).
func TestSecretVolumeHardening(t *testing.T) {
	docs := loadManifests(t)
	for _, name := range []struct{ kind, name string }{
		{"Deployment", "control-plane"},
		{"Deployment", "migration-review"},
		{"Job", "schema-migration"},
	} {
		d := findByName(docs, name.kind, name.name)
		if d == nil {
			t.Fatalf("%s %s missing", name.kind, name.name)
		}
		if !strings.Contains(d.Body, "fsGroup: 10001") {
			t.Errorf("%s: pod securityContext fsGroup 10001 missing", name.name)
		}
		lines := strings.Split(d.Body, "\n")
		for i, line := range lines {
			if !strings.Contains(line, "secretName:") {
				continue
			}
			hardened := false
			for j := i + 1; j <= i+2 && j < len(lines); j++ {
				if strings.Contains(lines[j], "defaultMode: 0400") {
					hardened = true
					break
				}
			}
			if !hardened {
				t.Errorf("%s: secret volume at line %d missing defaultMode: 0400", name.name, i+1)
			}
		}
	}
}

// TestControlPlanePreStopSleep SEC-0051: distroless has no shell, so preStop must not
// use exec; use the native K8s sleep action (PodLifecycleSleepAction, GA since 1.32).
// The contract termination.pre_stop sequence is handled by in-process graceful shutdown on SIGTERM.
func TestControlPlanePreStopSleep(t *testing.T) {
	docs := loadManifests(t)
	cp := findByName(docs, "Deployment", "control-plane")
	if cp == nil {
		t.Fatal("control-plane Deployment missing")
	}
	for _, frag := range []string{"preStop:", "sleep:", "seconds: 5"} {
		if !strings.Contains(cp.Body, frag) {
			t.Errorf("control-plane preStop sleep action %s missing", frag)
		}
	}
	if strings.Contains(cp.Body, "/bin/sh") || strings.Contains(cp.Body, "exec:") {
		t.Error("control-plane preStop must not use exec form (distroless has no shell)")
	}
}

// TestMigrationReviewListenContract SEC-0019: migration review surface defaults to loopback only
// (SEC-0043), so the container must pass --listen 0.0.0.0:9100 explicitly with aligned probe ports,
// otherwise kubelet probes cannot reach it and the Pod never becomes Ready.
func TestMigrationReviewListenContract(t *testing.T) {
	docs := loadManifests(t)
	mr := findByName(docs, "Deployment", "migration-review")
	if mr == nil {
		t.Fatal("migration-review Deployment missing")
	}
	for _, frag := range []string{"--listen", "0.0.0.0:9100", "containerPort: 9100"} {
		if !strings.Contains(mr.Body, frag) {
			t.Errorf("migration-review %s missing", frag)
		}
	}
	if strings.Contains(mr.Body, "containerPort: 8080") {
		t.Error("migration-review must not keep probe port 8080 (listen is 9100)")
	}
	// Migration mode routes already expose /api/v4/health/live|ready endpoints; probe paths stay unchanged and ports align.
	for _, frag := range []string{"/api/v4/health/live", "/api/v4/health/ready", "port: http"} {
		if !strings.Contains(mr.Body, frag) {
			t.Errorf("migration-review probe fragment %s missing", frag)
		}
	}
}

// TestSchemaMigrationJobApplyContract SEC-0019: schema Job must pass complete
// migrate apply arguments (--config mounting migration-config Secret, --migrations-dir/--catalog
// pointing to in-image paths) and must not reference YEARNING_DATABASE_DSN_FILE (unused in code, verified by audit).
func TestSchemaMigrationJobApplyContract(t *testing.T) {
	docs := loadManifests(t)
	job := findByName(docs, "Job", "schema-migration")
	if job == nil {
		t.Fatal("schema-migration Job missing")
	}
	if !strings.Contains(job.Body, `args: ["migrate", "apply"`) {
		t.Error(`schema Job args must start with ["migrate", "apply"]`)
	}
	for _, frag := range []string{
		"--config", "/var/run/secrets/yearning/migration-config/config.json",
		"--migrations-dir", "/usr/share/yearning/db-migrations",
		"--catalog", "/usr/share/yearning/contracts/postgresql-column-catalog.json",
		// NEW-DEPLOY-01: Job mounts its own schema-role config Secret (exact name).
		"secretName: yearning-migration-config-schema",
	} {
		if !strings.Contains(job.Body, frag) {
			t.Errorf("schema Job %s missing", frag)
		}
	}
	if strings.Contains(stripYAMLComments(job.Body), "YEARNING_DATABASE_DSN_FILE") {
		t.Error("schema Job must not set YEARNING_DATABASE_DSN_FILE (no Go reference)")
	}
}

// stripYAMLComments strips YAML comment lines — manifest comments may mention forbidden key names
// for documentation, while assertions target only effective configuration lines.
func stripYAMLComments(body string) string {
	var b strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// TestMigrationConfigRoleSplit NEW-DEPLOY-01 (20261022 fix): split migration config
// Secret by role — review Deployment mounts yearning-migration-config-review
// (workbench credential surface) and schema Job mounts yearning-migration-config-schema
// (migrator credential surface); the two workloads must not share the same config Secret, and each
// --config path must resolve under the Secret volume subpath actually mounted by that workload.
// (Background: SecretReference supports only absolute file paths or environment variables,
// so a single shared config Secret cannot satisfy target.password resolution for both workbench
// and migrator roles — legacy.LoadConfig enforces full structural validation on both sides.)
func TestMigrationConfigRoleSplit(t *testing.T) {
	docs := loadManifests(t)
	review := findByName(docs, "Deployment", "migration-review")
	job := findByName(docs, "Job", "schema-migration")
	if review == nil || job == nil {
		t.Fatal("migration workloads missing")
	}
	const configMountPath = "/var/run/secrets/yearning/migration-config"
	const configSubPath = "/var/run/secrets/yearning/migration-config/config.json"
	for _, tc := range []struct {
		name, body, want, notWant string
	}{
		{"migration-review", review.Body,
			"secretName: yearning-migration-config-review",
			"secretName: yearning-migration-config-schema"},
		{"schema-migration", job.Body,
			"secretName: yearning-migration-config-schema",
			"secretName: yearning-migration-config-review"},
	} {
		if !strings.Contains(tc.body, tc.want) {
			t.Errorf("%s: config Secret %s missing (role split NEW-DEPLOY-01)", tc.name, tc.want)
		}
		if strings.Contains(tc.body, tc.notWant) {
			t.Errorf("%s: must not mount the other role's config Secret %s", tc.name, tc.notWant)
		}
		// --config path must be a subpath of the mounted Secret volume.
		if !strings.Contains(tc.body, `mountPath: `+configMountPath) {
			t.Errorf("%s: config Secret volume mountPath %s missing", tc.name, configMountPath)
		}
		if !strings.Contains(tc.body, configSubPath) {
			t.Errorf("%s: --config must reference mounted Secret subpath %s", tc.name, configSubPath)
		}
	}
	// The cross-workload shared exact name yearning-migration-config must no longer appear
	// as a secretName (-review/-schema suffixes are unaffected).
	for _, d := range docs {
		if regexp.MustCompile(`(?m)^\s*secretName: yearning-migration-config\s*$`).
			MatchString(d.Body) {
			t.Errorf("%s %s: shared migration config Secret yearning-migration-config forbidden (role split)", d.Kind, d.Name)
		}
	}
}

// TestMigrationReviewServiceContract NEW-DEPLOY-03 (20261022 fix):
// migration review surface must have a matching Service routable by Ingress — single port 9100->http,
// ClusterIP, selector/labels aligned with the review Deployment; Service port must match container
// --listen 0.0.0.0:9100 (otherwise ingress namespace traffic allowed by NetworkPolicy has no routing
// endpoint and the review surface would require port-forward).
func TestMigrationReviewServiceContract(t *testing.T) {
	docs := loadManifests(t)
	mr := findByName(docs, "Deployment", "migration-review")
	if mr == nil {
		t.Fatal("migration-review Deployment missing")
	}
	// Port consistency anchor: container-side --listen must remain 0.0.0.0:9100.
	if !strings.Contains(mr.Body, "--listen\", \"0.0.0.0:9100") {
		t.Fatal("migration-review --listen 0.0.0.0:9100 missing; Service port assertion is meaningless")
	}
	svc := findByName(docs, "Service", "yearning-migration-review")
	if svc == nil {
		t.Fatal("Service yearning-migration-review missing (NEW-DEPLOY-03)")
	}
	for _, frag := range []string{
		"type: ClusterIP", "port: 9100", "targetPort: http",
	} {
		if !strings.Contains(svc.Body, frag) {
			t.Errorf("migration-review Service %s missing", frag)
		}
	}
	for _, sel := range []string{
		"app.kubernetes.io/name: yearning",
		"app.kubernetes.io/component: migration-review",
	} {
		if !strings.Contains(svc.Body, sel) {
			t.Errorf("migration-review Service selector/label %s missing", sel)
		}
	}
}

// yamlImages recursively collects all image values in a YAML document (B2: valid forms such as
// quoted keys, whitespace before colons, and flow mappings must not be missed by line regexes).
func yamlImages(body string) []string {
	var node any
	if err := yaml.Unmarshal([]byte(body), &node); err != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch typed := v.(type) {
		case map[string]any:
			for key, value := range typed {
				if key == "image" {
					if ref, ok := value.(string); ok {
						out = append(out, ref)
					} else {
						// Consistent with security_audit ambiguous semantics: non-string
						// image values must not be silently skipped.
						out = append(out, "\x00ambiguous-image")
					}
					continue
				}
				walk(value)
			}
		case []any:
			for _, value := range typed {
				walk(value)
			}
		}
	}
	walk(node)
	return out
}

// TestImageDigestPinned SEC-0016 (20261022 fix, conformance portion;
// security_audit.py --release-gate belongs to SUPPLY domain and hard-fails during release):
// all three image references (control-plane Deployment, migration-review Deployment,
// schema-migration Job) allow only two forms — pinned to a real @sha256:64-hex digest, or the
// sole repo-state placeholder @sha256:REPLACED_BY_RELEASE (rendered to a real digest by the Release
// workflow). Any unrendered ${...} variable, floating tag-only reference, or other placeholder fails
// immediately. Enforcing placeholder rendering is handled by the release workflow --release-gate
// (repo checkouts normally contain the placeholder, so failing on the placeholder here would break
// CI permanently — two-tier gate division: conformance guards shape drift, release-gate guards rendering).
func TestImageDigestPinned(t *testing.T) {
	docs := loadManifests(t)
	digestRe := regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
	const allowedRepo = "ghcr.io/cookiey/yearning@sha256:"
	sanctionedPlaceholder := regexp.MustCompile(`^ghcr\.io/cookiey/yearning@sha256:REPLACED_BY_RELEASE$`)
	for _, tc := range []struct{ kind, name string }{
		{"Deployment", "control-plane"},
		{"Deployment", "migration-review"},
		{"Job", "schema-migration"},
	} {
		d := findByName(docs, tc.kind, tc.name)
		if d == nil {
			t.Fatalf("%s %s missing", tc.kind, tc.name)
		}
		images := yamlImages(d.Body)
		if len(images) != 1 {
			t.Fatalf("%s %s: expected exactly 1 image reference, got %d", tc.kind, tc.name, len(images))
		}
		ref := images[0]
		if digestRe.MatchString(ref) && !strings.HasPrefix(ref, allowedRepo) {
			t.Errorf("%s %s: image %q points outside the trusted repository %q", tc.kind, tc.name, ref, strings.TrimSuffix(allowedRepo, "@sha256:"))
			continue
		}
		if digestRe.MatchString(ref) || sanctionedPlaceholder.MatchString(ref) {
			continue
		}
		switch {
		case strings.Contains(ref, "REPLACED_BY_RELEASE"):
			t.Errorf("%s %s: image %q misuses the release placeholder (must be the exact sanctioned form ghcr.io/cookiey/yearning@sha256:REPLACED_BY_RELEASE)", tc.kind, tc.name, ref)
		case strings.Contains(ref, "${"):
			t.Errorf("%s %s: image %q contains an unrendered variable", tc.kind, tc.name, ref)
		case !strings.Contains(ref, "@sha256:"):
			t.Errorf("%s %s: image %q is a floating tag reference; digest pin required", tc.kind, tc.name, ref)
		default:
			t.Errorf("%s %s: image %q is not a 64-hex sha256 digest pin", tc.kind, tc.name, ref)
		}
	}
}

// TestReleaseManifestsPinned SEC-0016/SEC-0019: release-rendered artifacts must replace the sole
// allowed placeholder with a real digest. Runs only when RELEASE_MANIFEST_DIR points to the rendered
// directory (make release-render-manifests / release workflow).
func TestReleaseManifestsPinned(t *testing.T) {
	dir := os.Getenv("RELEASE_MANIFEST_DIR")
	if dir == "" {
		t.Skip("RELEASE_MANIFEST_DIR not set; source manifests intentionally keep the sanctioned release placeholder")
	}
	docs := loadManifestsFrom(t, dir)
	if len(docs) == 0 {
		t.Fatalf("no rendered manifests under %s", dir)
	}
	pinned := regexp.MustCompile(`^ghcr\.io/cookiey/yearning@sha256:[0-9a-f]{64}$`)
	images := 0
	for _, d := range docs {
		for _, ref := range yamlImages(d.Body) {
			images++
			if strings.Contains(ref, "REPLACED_BY_RELEASE") {
				t.Errorf("%s %s: release placeholder survived rendering: %s", d.Kind, d.Name, ref)
			}
			if !pinned.MatchString(ref) {
				t.Errorf("%s %s: image not digest-pinned in rendered manifest: %s", d.Kind, d.Name, ref)
			}
		}
	}
	if images < 3 {
		t.Fatalf("expected >=3 rendered image references, got %d", images)
	}
}

package legacy

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// This file implements transformations for the 15 legacy models other than CoreAccount. Each function returns
// the target write operations or nil (when quarantined). Sole authority for field semantics: api/migration/legacy-field-mapping.json.

// CoreGlobalConfiguration -> identity_providers / notification_channels /
// system_settings / ai_providers (disabled candidates by default) / announcement_revisions.
// Returns false if any unknown JSON keys that block Apply are encountered.
func (tc *TransformContext) TransformGlobalConfig(row Row) (bool, error) {
	key := rowString(row["id"])
	ldapObj, ok := tc.parseJSONObject("core_global_configurations", key, "ldap", row["ldap"])
	if !ok {
		return false, nil
	}
	messageObj, ok := tc.parseJSONObject("core_global_configurations", key, "message", row["message"])
	if !ok {
		return false, nil
	}
	otherObj, ok := tc.parseJSONObject("core_global_configurations", key, "other", row["other"])
	if !ok {
		return false, nil
	}
	aiObj, ok := tc.parseJSONObject("core_global_configurations", key, "ai", row["ai"])
	if !ok {
		return false, nil
	}

	// Ldap -> identity_providers (typed configuration; secret sealed; disabled until admin connection test succeeds).
	if ldapURL := strings.TrimSpace(stringValue(ldapObj, "Url")); ldapURL != "" {
		secret := stringValue(ldapObj, "Password")
		cfg := map[string]any{
			"url": ldapURL, "user": stringValue(ldapObj, "User"),
			"type": stringValue(ldapObj, "Type"), "sc": stringValue(ldapObj, "Sc"),
		}
		cfgRaw := mustJSON(cfg)
		idp := IdentityProviderModel{
			ID:          tc.detID("identity_providers", "ldap").String(),
			ProviderKey: "ldap-legacy", ProviderKind: "ldap", DisplayName: "Legacy LDAP",
			Enabled:           false, // Disabled until a connection test succeeds
			Configuration:     cfgRaw,
			ConfigurationHash: tc.hash64("identity-config", []byte(cfgRaw)),
			SecretKeyID:       tc.Crypter.keyID, SecretAlgorithm: EnvelopeAlgorithm,
			CreatedByUserID: tc.builtinAdminID(), CreatedAt: tc.Now, UpdatedAt: tc.Now,
		}
		if secret != "" {
			sealed, err := tc.sealBytes("identity-secret", tc.aad("identity_providers", idp.ID, "secret", "provider_secret"), []byte(secret))
			if err != nil {
				return false, err
			}
			idp.SecretCiphertext, idp.SecretNonce = sealed.Ciphertext, sealed.Nonce
		}
		tc.addWrite("identity_providers", idp)
	}

	// Message -> email and DingTalk notification channels; secrets re-encrypted; disabled until tested.
	if host := strings.TrimSpace(stringValue(messageObj, "Host")); host != "" {
		cfg := map[string]any{"host": host, "port": intValue(messageObj, "Port"),
			"user": stringValue(messageObj, "User"), "to_user": stringValue(messageObj, "ToUser")}
		raw := mustJSON(cfg)
		ch := NotificationChannelModel{
			ID:   tc.detID("notification_channels", "email").String(),
			Kind: "email", Name: "Legacy Email", Enabled: false,
			Configuration: raw,
			SecretKeyID:   tc.Crypter.keyID, SecretAlgorithm: EnvelopeAlgorithm,
			CreatedByUserID: tc.builtinAdminID(), CreatedAt: tc.Now, UpdatedAt: tc.Now,
		}
		if pw := stringValue(messageObj, "Password"); pw != "" {
			sealed, err := tc.sealBytes("notification-secret", tc.aad("notification_channels", ch.ID, "secret", crypto.PurposePayload), []byte(pw))
			if err != nil {
				return false, err
			}
			ch.SecretCiphertext, ch.SecretNonce = sealed.Ciphertext, sealed.Nonce
		}
		tc.addWrite("notification_channels", ch)
	}
	if hook := strings.TrimSpace(stringValue(messageObj, "WebHook")); hook != "" {
		cfg := map[string]any{"webhook": hook}
		raw := mustJSON(cfg)
		dingID := tc.detID("notification_channels", "dingtalk").String()
		sealed, err := tc.sealBytes("notification-secret", tc.aad("notification_channels", dingID, "secret", crypto.PurposePayload), []byte(hook))
		if err != nil {
			return false, err
		}
		tc.addWrite("notification_channels", NotificationChannelModel{
			ID:   dingID,
			Kind: "dingtalk", Name: "Legacy DingTalk", Enabled: false,
			Configuration: raw, SecretCiphertext: sealed.Ciphertext, SecretNonce: sealed.Nonce,
			SecretKeyID: tc.Crypter.keyID, SecretAlgorithm: EnvelopeAlgorithm,
			CreatedByUserID: tc.builtinAdminID(), CreatedAt: tc.Now, UpdatedAt: tc.Now,
		})
	}

	// Other -> system_settings with field allowlist; unknown keys quarantine the row and block Apply.
	allowedOther := map[string]bool{
		"Limit": true, "IDC": true, "Query": true, "Register": true,
		"Export": true, "ExQueryTime": true,
	}
	filtered, unknown := allowlistFilter(otherObj, allowedOther)
	if !tc.ensureUnknownIsError("core_global_configurations", key, unknown) {
		return false, nil
	}
	settings := map[string]any{}
	for k, v := range filtered {
		settings[strings.ToLower(k)] = v
	}
	raw := mustJSON(settings)
	tc.addWrite("system_settings", SystemSettingModel{
		Namespace: "general", SchemaVersion: 1, Settings: raw,
		UpdatedByUserID: tc.builtinAdminID(), CreatedAt: tc.Now, UpdatedAt: tc.Now,
	})

	// AI -> ai_providers candidate (disabled with sealed API key).
	if baseURL := strings.TrimSpace(stringValue(aiObj, "BaseUrl")); baseURL != "" {
		cfg := map[string]any{
			"frequency_penalty": aiObj["FrequencyPenalty"], "max_tokens": aiObj["MaxTokens"],
			"presence_penalty": aiObj["PresencePenalty"], "temperature": aiObj["Temperature"],
			"top_p": aiObj["TopP"], "proxy_url": stringValue(aiObj, "ProxyURL"),
		}
		cfgRaw := mustJSON(cfg)
		p := AIProviderModel{
			ID:   tc.detID("ai_providers", "legacy-openai").String(),
			Name: "Legacy OpenAI", ProviderKind: "openai_compatible",
			BaseURL: baseURL, ModelName: stringValue(aiObj, "Model"),
			Enabled: false, SelectionPriority: 100,
			PrivacyContractHash: tc.hash64("privacy-contract", []byte("legacy:unverified")),
			OutputSchemaHash:    tc.hash64("output-schema", []byte("legacy:unverified")),
			Configuration:       cfgRaw,
			SecretKeyID:         tc.Crypter.keyID, SecretAlgorithm: EnvelopeAlgorithm,
			CreatedByUserID: tc.builtinAdminID(), CreatedAt: tc.Now, UpdatedAt: tc.Now,
		}
		if key2 := stringValue(aiObj, "APIKey"); key2 != "" {
			sealed, err := tc.sealBytes("ai-secret", tc.aad("ai_providers", p.ID, "secret_ciphertext", "provider_secret"), []byte(key2))
			if err != nil {
				return false, err
			}
			p.SecretCiphertext, p.SecretNonce = sealed.Ciphertext, sealed.Nonce
		}
		tc.addWrite("ai_providers", p)
	}

	// Board -> announcement revision (treated as untrusted text under current policy and HTML-escaped).
	if board := strings.TrimSpace(rowString(row["board"])); board != "" {
		safe := html.EscapeString(board)
		tc.addWrite("announcement_revisions", AnnouncementRevisionModel{
			ID:             tc.detID("announcement_revisions", "board").String(),
			RevisionNumber: 1, Title: "Legacy Board", MarkdownSource: board,
			SanitizedHTML:             safe,
			ContentSha256:             tc.hash64("announcement", []byte(board)),
			SanitizerPolicyVersion:    "legacy-escape:v1",
			CreatedByUserIDSnapshot:   tc.builtinAdminID(),
			CreatedByUsernameSnapshot: "admin", CreatedAt: tc.Now,
		})
	}

	// Authorization/Stmt/AuditRole -> archived in migration report without granting runtime authority.
	tc.Report["legacy_configuration"] = map[string]any{
		"authorization": tc.hash64("legacy-config", []byte(rowString(row["authorization"]))),
		"stmt":          tc.hash64("legacy-config", []byte(rowString(row["stmt"]))),
		"audit_role":    tc.hash64("legacy-config", []byte(rowString(row["audit_role"]))),
	}
	return true, nil
}

// CoreDataSource -> datasources + credentials + TLS material + candidates.
func (tc *TransformContext) TransformDataSource(row Row, ev *StatusEvidence) error {
	key := rowString(row["id"])
	sourceID := strings.TrimSpace(rowString(row["source_id"]))
	name := strings.TrimSpace(rowString(row["source"]))
	if name == "" {
		tc.quarantine("core_data_sources", key, "empty source name")
		return nil
	}
	dbType, _ := rowInt64(row["db_type"])
	var engine, compat string
	switch dbType {
	case 0:
		engine, compat = "mysql", "mysql"
	case 1:
		engine, compat = "postgresql", "postgresql"
	default:
		tc.quarantine("core_data_sources", key, fmt.Sprintf("db_type %d outside contract", dbType))
		return nil
	}
	host := strings.TrimSpace(rowString(row["ip"]))
	port, _ := rowInt64(row["port"])
	if host == "" || port < 1 || port > 65535 {
		tc.quarantine("core_data_sources", key, "invalid endpoint")
		return nil
	}
	isQuery, ok := rowInt64(row["is_query"])
	if !ok {
		tc.quarantine("core_data_sources", key, "is_query missing")
		return nil
	}
	switch isQuery {
	case 0, 1, 2:
	default:
		tc.quarantine("core_data_sources", key, fmt.Sprintf("is_query %d outside contract", isQuery))
		return nil
	}
	var dsID uuid.UUID = tc.detID("datasources", sourceIDOr(key, sourceID))
	idStr := dsID.String()
	meta := map[string]any{
		"idc":                rowString(row["idc"]),
		"exclude_db_list":    rowString(row["exclude_db_list"]),
		"insulate_word_list": rowString(row["insulate_word_list"]),
		"principal":          rowString(row["principal"]),
	}
	tc.addWrite("datasources", DatasourceModel{
		ID: idStr, Name: name, Engine: engine, CompatibilityMode: compat,
		DeploymentKind: "native", Host: host, Port: int(port),
		VersionConstraint: "legacy:unknown", Enabled: true,
		Metadata:        mustJSON(meta),
		CreatedByUserID: tc.builtinAdminID(), CreatedAt: tc.Now, UpdatedAt: tc.Now,
	})
	tc.Datasources[sourceID] = dsID
	tc.DSFingerprint[dsID] = tc.hash64("datasource-identity", []byte(strings.ToLower(engine+"|"+host+"|"+fmt.Sprint(port)+"|"+name)))

	// Credentials: plaintext is sealed directly in v4 format; ciphertext requires legacy_secret_key (quarantined if missing).
	// Payload format matches runtime credentialPayload JSON {username,password} (consumed by json.Unmarshal
	// in factory.OpenCredential) -- raw password bytes would be rejected as malformed by the runtime reader,
	// which would be equivalent to undecryptable ciphertext (covered by RCP-20260831 Option A).
	pw := rowString(row["password"])
	if pw != "" {
		plaintext, err := tc.resolveLegacyPassword(pw)
		if err != nil {
			tc.quarantine("core_data_sources", key, "encrypted password without legacy secret key")
			return nil
		}
		credPlain, err := json.Marshal(map[string]string{
			"username": strings.TrimSpace(rowString(row["username"])),
			"password": string(plaintext),
		})
		if err != nil {
			return err
		}
		credID := tc.detID("datasource_credentials", idStr).String()
		sealed, err := tc.sealBytes("datasource-credential", tc.aad("datasource_credentials", credID, "credential", "datasource_credential"), credPlain)
		if err != nil {
			return err
		}
		tc.addWrite("datasource_credentials", DatasourceCredentialModel{
			ID:           credID,
			DatasourceID: idStr, Purpose: "execution",
			CredentialCiphertext: sealed.Ciphertext, CredentialNonce: sealed.Nonce,
			CredentialKeyID: sealed.KeyID, CredentialAlgorithm: sealed.Algorithm,
			Enabled: true, CreatedAt: tc.Now, UpdatedAt: tc.Now,
		})
	}
	// TLS material: sealed when all three PEM components are present. Plaintext format is a concatenated PEM bundle
	// (CA chain -> client cert -> private key) matching the runtime splitTLS* parser in factory/execution/query
	// (BE-B13 alignment: previously wrote a JSON bundle that runtime consumers could not parse).
	ca := rowString(row["ca_file"])
	cert := rowString(row["cert"])
	keyFile := rowString(row["key_file"])
	if ca != "" || cert != "" || keyFile != "" {
		if ca == "" || cert == "" || keyFile == "" {
			tc.quarantine("core_data_sources", key, "incomplete tls bundle")
			return nil
		}
		bundle := ca + "\n" + cert + "\n" + keyFile
		tlsID := tc.detID("datasource_tls_materials", idStr).String()
		sealed, err := tc.sealBytes("datasource-tls", tc.aad("datasource_tls_materials", tlsID, "material", "datasource_tls"), []byte(bundle))
		if err != nil {
			return err
		}
		tc.addWrite("datasource_tls_materials", DatasourceTLSMaterialModel{
			ID:           tlsID,
			DatasourceID: idStr, Purpose: "execution", VerifyServer: true,
			MaterialCiphertext: sealed.Ciphertext, MaterialNonce: sealed.Nonce,
			MaterialKeyID: sealed.KeyID, MaterialAlgorithm: sealed.Algorithm,
			CreatedAt: tc.Now, UpdatedAt: tc.Now,
		})
	}
	// FlowID/RuleId -> candidate records (not written directly onto the datasource).
	if flowID := strings.TrimSpace(rowString(row["flow_id"])); flowID != "" || strings.TrimSpace(rowString(row["rule_id"])) != "" {
		tc.appendCandidate(ManifestCandidate{
			CandidateID: tc.detID("candidate", "ds-binding|"+key).String(),
			Kind:        "single_stage_flow", Risk: "possible_expansion",
			SourceRefs:           []string{"core_data_sources:" + key},
			TargetDefinitionHash: tc.phash("candidate-ds", []byte(key+"|"+rowString(row["flow_id"])+"|"+rowString(row["rule_id"]))),
		})
	}
	return nil
}

// resolveLegacyPassword determines whether a legacy password is stored as plaintext or ciphertext.
// Contracted behavior: if the value is base64-encoded, block-aligned, and decrypts cleanly via LegacyDecrypt,
// return the decrypted plaintext; otherwise treat as stored plaintext (legacy impl.go:50 stored plaintext in some paths).
func (tc *TransformContext) resolveLegacyPassword(stored string) ([]byte, error) {
	if plain, err := tc.Crypter.LegacyDecrypt(stored); err == nil {
		return plain, nil
	}
	// When the legacy secret key is not configured and the value looks like base64 ciphertext (len >= 16 and len%4 == 0), never guess plaintext.
	if len(tc.Crypter.legacySecretKey) == 0 && looksLikeBase64(stored) {
		return nil, ErrLegacyKeyMissing
	}
	return []byte(stored), nil
}

func looksLikeBase64(s string) bool {
	if len(s) < 16 || len(s)%4 != 0 {
		return false
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/="
	for _, r := range s {
		if !strings.ContainsRune(alphabet, r) {
			return false
		}
	}
	return true
}

// CoreRoleGroup -> permission_groups + flow grant candidates.
func (tc *TransformContext) TransformRoleGroup(row Row) error {
	key := rowString(row["id"])
	name := strings.TrimSpace(rowString(row["name"]))
	if name == "" {
		tc.quarantine("core_role_groups", key, "empty group name")
		return nil
	}
	groupID := tc.detID("permission_groups", rowString(row["group_id"]))
	tc.addWrite("permission_groups", PermissionGroupModel{
		ID: groupID.String(), Name: tc.uniqueName(name), Enabled: true,
		CreatedByUserID: tc.builtinAdminID(), CreatedAt: tc.Now, UpdatedAt: tc.Now,
	})
	// Legacy groups are indexed by both Name and GroupId so member expansion resolves either reference.
	tc.groupIDs[name] = groupID
	if gid := strings.TrimSpace(rowString(row["group_id"])); gid != "" {
		tc.groupIDs[gid] = groupID
	}
	permObj, ok := tc.parseJSONObject("core_role_groups", key, "permissions", row["permissions"])
	if !ok {
		return nil
	}
	allowed := map[string]bool{"DDLSource": true, "DMLSource": true, "QuerySource": true}
	filtered, unknown := allowlistFilter(permObj, allowed)
	if !tc.ensureUnknownIsError("core_role_groups", key, unknown) {
		return nil
	}
	def := map[string]any{
		"ddl_source": filtered["DDLSource"], "dml_source": filtered["DMLSource"],
		"query_source": filtered["QuerySource"],
	}
	tc.appendCandidate(ManifestCandidate{
		CandidateID: tc.detID("candidate", "group-grant|"+key).String(),
		Kind:        "permission_group_flow_grant", Risk: "possible_expansion",
		SourceRefs:           []string{"core_role_groups:" + key},
		TargetDefinitionHash: tc.phash("candidate-grant", []byte(mustJSON(def))),
	})
	tc.groupGrants[groupID.String()] = def
	return nil
}

// CoreGrained -> permission_group_members.
func (tc *TransformContext) TransformGrained(row Row) error {
	key := rowString(row["id"])
	username := strings.TrimSpace(rowString(row["username"]))
	uid, ok := tc.Users[strings.ToLower(username)]
	if !ok {
		tc.quarantine("core_graineds", key, "username unresolved")
		return nil
	}
	var groups []string
	text := rowString(row["group"])
	if err := jsonUnmarshalArray(text, &groups); err != nil {
		tc.quarantine("core_graineds", key, "group json parse failed")
		return nil
	}
	for _, g := range groups {
		gid, ok := tc.groupIDs[g]
		if !ok {
			if parsed, err := parseUUID(g); err == nil {
				gid = parsed
			} else {
				tc.quarantine("core_graineds", key, "unknown group "+tc.hash64("sample", []byte(g)))
				return nil
			}
		}
		tc.addWrite("permission_group_members", PermissionGroupMemberModel{
			PermissionGroupID: gid.String(), UserID: uid.String(),
		})
	}
	return nil
}

// CoreWorkflowTpl -> flow candidate (single-stage by default).
func (tc *TransformContext) TransformWorkflowTpl(row Row) error {
	key := rowString(row["id"])
	sourceID := strings.TrimSpace(rowString(row["source"]))
	ds, ok := tc.Datasources[sourceID]
	if !ok {
		tc.quarantine("core_workflow_tpls", key, "datasource unresolved")
		return nil
	}
	stepsObj, ok := tc.parseJSONObject("core_workflow_tpls", key, "steps", row["steps"])
	if !ok && row["steps"] != nil {
		// Also quarantine when steps is a JSON array rather than an object.
		if !isArrayJSON(rowString(row["steps"])) {
			return nil
		}
		stepsObj = map[string]any{}
	}
	multi := len(stepsObj) > 1 || strings.Contains(rowString(row["steps"]), ",")
	def := map[string]any{
		"datasource_id":  ds.String(),
		"stages":         1,
		"source":         sourceID,
		"steps_raw_hash": tc.hash64("flow-steps", []byte(rowString(row["steps"]))),
	}
	tc.appendCandidate(ManifestCandidate{
		CandidateID:          tc.detID("candidate", "flow|"+key).String(),
		Kind:                 map[bool]string{true: "multi_stage_flow", false: "single_stage_flow"}[multi],
		Risk:                 "possible_expansion",
		SourceRefs:           []string{"core_workflow_tpls:" + key},
		TargetDefinitionHash: tc.phash("candidate-flow", []byte(mustJSON(def))),
	})
	tc.flowTemplates = append(tc.flowTemplates, flowTemplateRef{Key: key, Datasource: ds, Multi: multi, Source: sourceID})
	return nil
}

// CoreRules -> rule_sets (with AuditRole recorded as a candidate).
func (tc *TransformContext) TransformRules(row Row) error {
	key := rowString(row["id"])
	desc := strings.TrimSpace(rowString(row["desc"]))
	if desc == "" {
		desc = "legacy-rule-" + key
	}
	rulesObj, ok := tc.parseJSONObject("core_rules", key, "audit_role", row["audit_role"])
	if !ok {
		return nil
	}
	rulesRaw := mustJSON(rulesObj)
	id := tc.detID("rule_sets", key)
	tc.addWrite("rule_sets", RuleSetModel{
		ID: id.String(), Name: tc.uniqueName(desc), Enabled: true,
		Rules: rulesRaw, ConfigHash: tc.hash64("rules", []byte(rulesRaw)),
		CreatedByUserID: tc.builtinAdminID(), CreatedAt: tc.Now, UpdatedAt: tc.Now,
	})
	tc.appendCandidate(ManifestCandidate{
		CandidateID: tc.detID("candidate", "rules|"+key).String(),
		Kind:        "rule_set", Risk: "unmapped",
		SourceRefs:           []string{"core_rules:" + key},
		TargetDefinitionHash: tc.phash("candidate-rules", []byte(rulesRaw)),
	})
	tc.ruleSets = append(tc.ruleSets, id)
	return nil
}

// Transformations for historical models are implemented in transforms_history.go.

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

func stringValue(obj map[string]any, key string) string {
	if v, ok := obj[key]; ok {
		return rowString(v)
	}
	return ""
}

func intValue(obj map[string]any, key string) int64 {
	if n, ok := rowInt64(obj[key]); ok {
		return n
	}
	return 0
}

func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func jsonUnmarshalArray(text string, out *[]string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return json.Unmarshal([]byte(text), out)
}

func isArrayJSON(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]")
}

func parseUUID(s string) (uuid.UUID, error) {
	return uuid.Parse(strings.TrimSpace(s))
}

func sourceIDOr(key, sourceID string) string {
	if strings.TrimSpace(sourceID) != "" {
		return sourceID
	}
	return "id:" + key
}

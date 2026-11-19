package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/google/uuid"
)

func toolRequestSignature(request provider.ToolRequest) (string, error) {
	value, err := json.Marshal(struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}{request.ToolName, request.Arguments})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:]), nil
}

// The model ID is correlation data within one stage, never a global DB key.
func storedToolCallID(runID, stageID, providerID, signature string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(runID+"\x00"+stageID+"\x00"+providerID+"\x00"+signature)).String()
}

package application

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

const stageReviewContextTable = "stage_review_snapshots"

func stageReviewContextRef(runID string, stagePosition int32) crypto.ColumnRef {
	return crypto.ColumnRef{
		Table:      stageReviewContextTable,
		PrimaryKey: runID + "#" + fmt.Sprint(stagePosition),
		Column:     "normalized_context_ciphertext",
	}
}

// openStageReviewContext preserves the existing normalized context and its
// ordinary JSON number shapes while the stage adds accepted model output.
func openStageReviewContext(
	c *crypto.Crypter, runID string, stagePosition int32, env crypto.Envelope,
) (map[string]any, error) {
	plain, err := c.OpenColumn(crypto.PurposePayload, stageReviewContextRef(runID, stagePosition), env)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.UseNumber()
	value := map[string]any{}
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if value == nil {
		value = map[string]any{}
	}
	return value, nil
}

func sealStageReviewContext(
	c *crypto.Crypter, runID string, stagePosition int32, value map[string]any,
) (crypto.Envelope, error) {
	plain, err := json.Marshal(value)
	if err != nil {
		return crypto.Envelope{}, err
	}
	return c.SealColumn(crypto.PurposePayload, stageReviewContextRef(runID, stagePosition), plain)
}

func acceptedSummaryAndLocaleFromStageContext(
	c *crypto.Crypter, runID string, stagePosition int32, env crypto.Envelope,
) (*string, *string, error) {
	value, err := openStageReviewContext(c, runID, stagePosition, env)
	if err != nil {
		return nil, nil, err
	}
	summary, ok := value["accepted_summary"].(string)
	if !ok || summary == "" {
		return nil, nil, nil
	}
	locale, ok := value["accepted_output_locale"].(string)
	if !ok || (locale != "zh-CN" && locale != "en-US") {
		return nil, nil, errEnumUnknown
	}
	return &summary, &locale, nil
}

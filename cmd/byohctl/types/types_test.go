// types/types_test.go
package types

import (
	"encoding/json"
	"testing"
)

func TestSecretUnmarshal(t *testing.T) {
	// Test JSON unmarshaling of Secret
	jsonData := []byte(`{
		"apiVersion": "v1",
		"kind": "Secret",
		"metadata": {
			"name": "test-secret",
			"namespace": "default"
		},
		"data": {
			"key1": "value1",
			"key2": "value2"
		}
	}`)

	var secret Secret
	err := json.Unmarshal(jsonData, &secret)
	if err != nil {
		t.Fatalf("Failed to unmarshal Secret: %v", err)
	}

	// Verify data
	if len(secret.Data) != 2 {
		t.Errorf("Expected 2 data items, got %d", len(secret.Data))
	}

	if secret.Data["key1"] != "value1" {
		t.Errorf("Expected Data[\"key1\"] = \"value1\", got %q", secret.Data["key1"])
	}

	if secret.Data["key2"] != "value2" {
		t.Errorf("Expected Data[\"key2\"] = \"value2\", got %q", secret.Data["key2"])
	}
}

func TestTokenResponseUnmarshal(t *testing.T) {
	// Test JSON unmarshaling of TokenResponse
	jsonData := []byte(`{
		"id_token": "test-token"
	}`)

	var tokenResponse TokenResponse
	err := json.Unmarshal(jsonData, &tokenResponse)
	if err != nil {
		t.Fatalf("Failed to unmarshal TokenResponse: %v", err)
	}

	// Verify token
	if tokenResponse.IDToken != "test-token" {
		t.Errorf("Expected IDToken = \"test-token\", got %q", tokenResponse.IDToken)
	}
}

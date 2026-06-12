package keypool

import (
	"path/filepath"
	"testing"
)

func TestImportBackupRestoresConfig(t *testing.T) {
	pool, err := New(filepath.Join(t.TempDir(), "mimo.db"), "https://default.example.com/v1/chat/completions")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer pool.Close()

	backup := &BackupData{
		Version: 1,
		Channels: []ChannelInfo{
			{
				ID:        42,
				Name:      "Imported",
				Prefix:    "imported",
				BaseURL:   "https://api.example.com/openai/v1/chat/completions",
				ProxyURL:  "http://user:pass@127.0.0.1:8080",
				Enabled:   true,
				IsDefault: true,
				PinnedKey: "upstream-key",
				KeyMode:   "failover",
				CreatedAt: "2026-01-01 00:00:00",
			},
		},
		Keys: []KeyInfo{
			{
				Key:              "upstream-key",
				ChannelID:        42,
				Note:             "primary account",
				DefaultModel:     "mimo-v2.5-pro",
				UsageCount:       7,
				PromptTokens:     11,
				CompletionTokens: 13,
				TotalTokens:      24,
				Enabled:          true,
				CreatedAt:        "2026-01-01 00:00:01",
			},
		},
		ProxyKeys: []ProxyKeyInfo{
			{
				Key:       "sk-client",
				Note:      "client app",
				Enabled:   true,
				CreatedAt: "2026-01-01 00:00:02",
			},
		},
		ModelMappings: []ModelMapping{
			{
				ID:          1,
				Name:        "test-model",
				ChannelID:   42,
				TargetModel: "real-model",
				Note:        "test mapping",
				Enabled:     true,
				CreatedAt:   "2026-01-01 00:00:03",
			},
		},
		Settings: map[string]string{"retention": "short"},
	}

	summary, err := pool.ImportBackup(backup)
	if err != nil {
		t.Fatalf("ImportBackup() error = %v", err)
	}
	if summary.Channels != 1 || summary.Keys != 1 || summary.ProxyKeys != 1 || summary.ModelMappings != 1 || summary.Settings != 1 {
		t.Fatalf("ImportBackup() summary = %+v", summary)
	}

	exported, err := pool.ExportBackup()
	if err != nil {
		t.Fatalf("ExportBackup() error = %v", err)
	}

	var importedChannel *ChannelInfo
	for i := range exported.Channels {
		if exported.Channels[i].Prefix == "imported" {
			importedChannel = &exported.Channels[i]
			break
		}
	}
	if importedChannel == nil {
		t.Fatal("imported channel was not exported")
	}
	if !importedChannel.IsDefault || importedChannel.KeyMode != "failover" || importedChannel.PinnedKey != "upstream-key" {
		t.Fatalf("imported channel = %+v", *importedChannel)
	}
	if importedChannel.ProxyURL != "http://user:pass@127.0.0.1:8080" {
		t.Fatalf("imported channel proxy_url = %q", importedChannel.ProxyURL)
	}

	var importedKey *KeyInfo
	for i := range exported.Keys {
		if exported.Keys[i].Key == "upstream-key" {
			importedKey = &exported.Keys[i]
			break
		}
	}
	if importedKey == nil {
		t.Fatal("imported key was not exported")
	}
	if importedKey.ChannelID != importedChannel.ID || importedKey.DefaultModel != "mimo-v2.5-pro" || importedKey.TotalTokens != 24 {
		t.Fatalf("imported key = %+v, channel = %+v", *importedKey, *importedChannel)
	}
	if !pool.ValidateProxyKey("sk-client") {
		t.Fatal("imported proxy key is not valid")
	}
	var importedMapping *ModelMapping
	for i := range exported.ModelMappings {
		if exported.ModelMappings[i].Name == "test-model" {
			importedMapping = &exported.ModelMappings[i]
			break
		}
	}
	if importedMapping == nil {
		t.Fatal("imported model mapping was not exported")
	}
	if importedMapping.TargetModel != "real-model" || importedMapping.ChannelID != importedChannel.ID {
		t.Fatalf("imported mapping = %+v", *importedMapping)
	}
	if got := pool.GetSetting("retention", ""); got != "short" {
		t.Fatalf("GetSetting(retention) = %q", got)
	}
}

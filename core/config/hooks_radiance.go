package config

func init() {
	RegisterBackendHook("radiance", radianceDefaults)
}

// radianceDefaults makes chat requests reach Radiance as structured messages.
// Radiance applies the model's own Jinja chat template, which it reports over
// its OpenAI-compatible API; without the tokenizer template core renders the
// prompt itself with no template, so that template and the upstream tool and
// reasoning handling are bypassed.
//
// Only configs that declare the chat usecase get it: usecase guessing reads the
// tokenizer template as "this model chats", so setting it on a config with any
// other usecase would offer that model to chat pickers. A config that brings
// its own templates keeps them.
func radianceDefaults(cfg *ModelConfig, _ string) {
	t := cfg.TemplateConfig
	if t.UseTokenizerTemplate || t.Chat != "" || t.ChatMessage != "" || t.Completion != "" || t.Edit != "" {
		return
	}
	declared := GetUsecasesFromYAML(cfg.KnownUsecaseStrings)
	if declared == nil || *declared&FLAG_CHAT != FLAG_CHAT {
		return
	}
	cfg.TemplateConfig.UseTokenizerTemplate = true
}

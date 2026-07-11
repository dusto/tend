package acp

import "github.com/dusto/tend/api"

type configAxis struct {
	apply    func(*Session, acpConfigOption)
	configID func(*Session) string
	write    func(ConfigSink, api.SessionID, string)
	event    func(api.SessionID, string) api.Event
}

var configAxes = map[string]configAxis{
	configCategoryMode: {
		apply: func(s *Session, opt acpConfigOption) {
			s.CurrentModeID = opt.CurrentValue
			s.AvailableModes = toAPIModesFromOptions(opt.Options)
			s.modeConfigID = opt.ID
		},
		configID: func(s *Session) string { return s.modeConfigID },
		write:    func(sink ConfigSink, id api.SessionID, value string) { sink.SetSessionMode(id, value) },
		event: func(id api.SessionID, value string) api.Event {
			return sessionEvent(string(id), "agent_mode_updated", api.AgentModeUpdated{
				SessionID:     id,
				CurrentModeID: value,
			})
		},
	},
	configCategoryModel: {
		apply: func(s *Session, opt acpConfigOption) {
			s.CurrentModelID = opt.CurrentValue
			s.AvailableModels = toAPIModelsFromOptions(opt.Options)
			s.modelConfigID = opt.ID
		},
		configID: func(s *Session) string { return s.modelConfigID },
		write:    func(sink ConfigSink, id api.SessionID, value string) { sink.SetSessionModel(id, value) },
		event: func(id api.SessionID, value string) api.Event {
			return sessionEvent(string(id), "agent_model_updated", api.AgentModelUpdated{
				SessionID:      id,
				CurrentModelID: value,
			})
		},
	},
	configCategoryThoughtLevel: {
		apply: func(s *Session, opt acpConfigOption) {
			s.CurrentThoughtLevelID = opt.CurrentValue
			s.AvailableThoughtLevels = toAPIThoughtLevelsFromOptions(opt.Options)
			s.thoughtConfigID = opt.ID
		},
		configID: func(s *Session) string { return s.thoughtConfigID },
		write:    func(sink ConfigSink, id api.SessionID, value string) { sink.SetSessionThoughtLevel(id, value) },
		event: func(id api.SessionID, value string) api.Event {
			return sessionEvent(string(id), "agent_thought_level_updated", api.AgentThoughtLevelUpdated{
				SessionID:             id,
				CurrentThoughtLevelID: value,
			})
		},
	},
}

func configAxisForCategory(category string) (configAxis, bool) {
	axis, ok := configAxes[canonicalCategory(category)]
	return axis, ok
}

func applySessionConfigOption(s *Session, opt acpConfigOption) bool {
	axis, ok := configAxisForCategory(opt.Category)
	if !ok {
		return false
	}
	axis.apply(s, opt)
	return true
}

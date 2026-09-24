package engine

import (
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	"github.com/woyin/orangeguard/internal/config"
)

// ModelRegistration builds the model.register response that publishes every
// virtual model, with its declared capabilities, into the cpa model list.
func ModelRegistration(cfg config.Config) pluginapi.ModelRegistrationResponse {
	resp := pluginapi.ModelRegistrationResponse{Provider: cfg.Provider}
	if !cfg.Enabled {
		return resp
	}
	created := time.Now().Unix()
	for _, vm := range cfg.VirtualModels {
		caps := vm.Capabilities
		info := pluginapi.ModelInfo{
			ID:                         vm.Name,
			Object:                     "model",
			Created:                    created,
			OwnedBy:                    firstNonEmpty(caps.OwnedBy, cfg.Provider),
			Type:                       caps.Type,
			DisplayName:                firstNonEmpty(caps.DisplayName, vm.Name),
			Name:                       vm.Name,
			Description:                firstNonEmpty(caps.Description, describe(vm)),
			ContextLength:              caps.ContextLength,
			InputTokenLimit:            firstPositive(caps.InputTokenLimit, caps.ContextLength),
			MaxCompletionTokens:        caps.MaxOutputTokens,
			OutputTokenLimit:           caps.MaxOutputTokens,
			SupportedInputModalities:   caps.InputModalities,
			SupportedOutputModalities:  caps.OutputModalities,
			SupportedParameters:        caps.SupportedParameters,
			SupportedGenerationMethods: caps.GenerationMethods,
			UserDefined:                true,
		}
		if t := caps.Thinking; t != nil {
			info.Thinking = &pluginapi.ThinkingSupport{
				Min:            t.Min,
				Max:            t.Max,
				ZeroAllowed:    t.ZeroAllowed,
				DynamicAllowed: t.DynamicAllowed,
				Levels:         t.Levels,
			}
		}
		resp.Models = append(resp.Models, info)
	}
	return resp
}

func describe(vm config.VirtualModel) string {
	names := ""
	for i, m := range vm.Members {
		if i > 0 {
			names += ", "
		}
		names += m.Model
	}
	return "orangeguard virtual model (" + vm.Strategy + "): " + names
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstPositive(values ...int64) int64 {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

package parser

import "context"

type importOnlyProviderFactory struct {
	def AgentDef
}

func newImportOnlyProviderFactory(def AgentDef) ProviderFactory {
	return importOnlyProviderFactory{def: cloneAgentDef(def)}
}

func (f importOnlyProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f importOnlyProviderFactory) Capabilities() Capabilities {
	return Capabilities{}
}

func (f importOnlyProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &importOnlyProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Config: cfg,
		},
	}
}

type importOnlyProvider struct {
	ProviderBase
}

func (p *importOnlyProvider) Parse(
	context.Context,
	ParseRequest,
) (ParseOutcome, error) {
	return ParseOutcome{}, p.unsupported(ProviderFeatureParse)
}

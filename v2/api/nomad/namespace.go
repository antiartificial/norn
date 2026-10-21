package nomad

// An empty InfraSpec namespace preserves the existing default placement.
// Every managed effect carries the effective value explicitly in Nomad IO.
func effectiveNomadNamespace(namespace string) string {
	if namespace == "" {
		return "default"
	}
	return namespace
}

func namespaceOption(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return effectiveNomadNamespace(values[0])
}

func jobNamespace(jobNamespace *string) string {
	if jobNamespace == nil {
		return "default"
	}
	return effectiveNomadNamespace(*jobNamespace)
}

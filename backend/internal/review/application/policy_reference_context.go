package application

func clonePolicyMetadataScope(prior metadataScope) metadataScope {
	out := newMetadataScope()
	for schema, allowed := range prior.schemas {
		out.schemas[schema] = allowed
	}
	for group, allowed := range prior.groups {
		out.groups[group] = allowed
	}
	for schema, tables := range prior.objects {
		out.objects[schema] = map[string]bool{}
		for table, allowed := range tables {
			out.objects[schema][table] = allowed
		}
	}
	return out
}

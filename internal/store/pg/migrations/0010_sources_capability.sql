-- fetch_sources / search_materials moved from "research" to their own "sources" capability, so an
-- agent can research the web without reading CueFlow's collected material. Agents that had
-- research keep both tools.
UPDATE agents SET capabilities = array_append(capabilities, 'sources')
WHERE 'research' = ANY(capabilities) AND NOT ('sources' = ANY(capabilities));

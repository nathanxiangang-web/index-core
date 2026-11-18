-- Exact priorQuery template; $1 is the bound Root UUID. No scope narrowing.
SELECT c.resource_id::text, c.root_id::text, c.introduced_at_generation, c.last_confirmed_generation,
		        c.resource_presence, c.removal_evidence_state, c.missing_since, c.consecutive_complete_missing,
		        c.missing_first_snapshot_id::text, c.missing_last_snapshot_id::text, c.canonical_path,
		        c.parent_resource_id::text, c.name, c.is_dir, c.size, c.mtime,
		        c.content_hash, c.hash_algorithm, c.content_type, c.current_attributes,
		        c.created_at, c.updated_at,
		        o.provider_object_id, o.provider_object_id_scope, o.provider_identity_assurance
		   FROM index_canonical_resource c
		   LEFT JOIN LATERAL (
		       SELECT provider_object_id, provider_object_id_scope, provider_identity_assurance
		         FROM index_identity_evidence_observation
		        WHERE resource_id = c.resource_id
		        ORDER BY observation_id DESC LIMIT 1
		   ) o ON true
		  WHERE c.root_id=$1::uuid ORDER BY c.resource_id;

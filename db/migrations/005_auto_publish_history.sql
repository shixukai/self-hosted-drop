-- Link previously accepted automatic intents without submitting another publish.
UPDATE deployments d SET auto_publication_id=p.id
 FROM idempotency_records i JOIN publications p ON p.id=i.resource_id
 WHERE d.status='READY' AND d.metadata->>'publishAfterReady'='true'
 AND d.auto_publication_id IS NULL AND d.auto_publish_error_code=''
 AND i.actor_id=d.created_by AND i.operation='publication:'||d.site_id
 AND i.key='auto-'||d.id AND p.site_id=d.site_id AND p.deployment_id=d.id;

-- Old DONE jobs may have swallowed a refusal. Preserve READY and make this
-- uncertainty explicit instead of showing an automatic publish as pending forever.
UPDATE deployments d SET auto_publish_error_code='AUTO_PUBLISH_OUTCOME_UNKNOWN',
 auto_publish_error_message='历史自动发布结果未记录，请核对站点和发布记录后明确重新发布'
 WHERE d.status='READY' AND d.metadata->>'publishAfterReady'='true'
 AND d.auto_publication_id IS NULL AND d.auto_publish_error_code=''
 AND EXISTS(SELECT 1 FROM jobs j WHERE j.kind='PROCESS' AND j.resource_id=d.id AND j.status='DONE');

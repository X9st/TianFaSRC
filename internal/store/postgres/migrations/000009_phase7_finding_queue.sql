CREATE INDEX findings_org_status_idx ON findings(organization_id,status);
CREATE INDEX findings_review_queue_idx ON findings(
  organization_id,
  (CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END),
  created_at DESC,
  id DESC
) WHERE status='draft';

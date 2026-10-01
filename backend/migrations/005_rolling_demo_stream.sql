UPDATE demo_stream_state
SET enabled = TRUE,
    updated_at = NOW()
WHERE singleton = TRUE
  AND enabled = FALSE
  AND emitted_since_reset = 500
  AND next_offset = 501;

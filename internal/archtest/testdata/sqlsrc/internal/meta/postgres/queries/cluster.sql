-- name: BumpEpoch :one
UPDATE cluster_epoch SET epoch = epoch + 1 WHERE area = $1 RETURNING epoch;

-- name: ReadEpochs :many
SELECT area, epoch FROM cluster_epoch;

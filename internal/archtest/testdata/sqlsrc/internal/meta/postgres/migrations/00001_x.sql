-- +goose Up
SET lock_timeout = '5s';
CREATE TEMP TABLE scratch (id int);
INSERT INTO cluster_epoch (area, epoch) VALUES ('repo_config', 0);

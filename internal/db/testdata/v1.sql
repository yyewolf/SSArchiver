-- SSArchiver v1 database, schema exactly as created by the 2026-10-08 models,
-- seeded with one row per case the platform migration must carry over:
-- a polled player with a finished backfill, a never-polled player, and a
-- disabled player with a running backfill, a retry time and an error;
-- scores in every replay state; a user, a session, a setting and an event.
CREATE TABLE `players` (`id` text,`name` text NOT NULL,`avatar_url` text NOT NULL,`country` text NOT NULL,`enabled` numeric NOT NULL,`added_at` datetime NOT NULL,`last_polled_at` datetime,`last_error` text NOT NULL,`backfill_state` text NOT NULL,`backfill_page` integer NOT NULL,`backfill_total_pages` integer NOT NULL,`backfill_retry_at` datetime,PRIMARY KEY (`id`));
INSERT INTO players VALUES('76561198038925092','Yewolf','https://cdn.scoresaber.com/avatars/a.jpg','FR',1,'2026-10-08T10:00:00Z','2026-10-10T09:00:00Z','','done',12,11,NULL);
INSERT INTO players VALUES('2169974796454690','Never polled','https://cdn.scoresaber.com/avatars/b.jpg','US',1,'2026-10-09T10:00:00Z',NULL,'','pending',1,0,NULL);
INSERT INTO players VALUES('111','Gone','https://cdn.scoresaber.com/avatars/c.jpg','DE',0,'2026-10-09T11:00:00Z','2026-10-09T12:00:00Z','player not found on ScoreSaber; tracking disabled','running',7,40,'2026-10-09T12:05:00Z');
CREATE TABLE `leaderboards` (`id` integer,`song_hash` text NOT NULL,`song_name` text NOT NULL,`song_sub_name` text NOT NULL,`song_author` text NOT NULL,`mapper` text NOT NULL,`difficulty` integer NOT NULL,`difficulty_raw` text NOT NULL,`game_mode` text NOT NULL,`cover_url` text NOT NULL,`status` text NOT NULL,`stars` real NOT NULL,`max_score` integer NOT NULL,PRIMARY KEY (`id`));
INSERT INTO leaderboards VALUES(1001,'4640065298E79DC3D61A15695AEB7FED95B42B30','Song A','','Art','Map',9,'_ExpertPlus_SoloStandard','SoloStandard','https://cdn.scoresaber.com/covers/a.png','RANKED',10.5,1000);
INSERT INTO leaderboards VALUES(1002,'4850C7BC85D89F832A96C6036347773E3858CED7','Song B','','Art','Map',7,'_Expert_SoloOneSaber','SoloOneSaber','https://cdn.scoresaber.com/covers/b.png','UNRANKED',0.0,900);
CREATE TABLE `scores` (`id` integer,`player_id` text NOT NULL,`leaderboard_id` integer NOT NULL,`rank` integer NOT NULL,`modified_score` integer NOT NULL,`unmodified_score` integer NOT NULL,`accuracy` real NOT NULL,`pp` real NOT NULL,`mods` text NOT NULL,`full_combo` numeric NOT NULL,`missed_notes` integer NOT NULL,`bad_cuts` integer NOT NULL,`max_combo` integer NOT NULL,`hmd` text NOT NULL,`personal_best` numeric NOT NULL,`set_at` datetime NOT NULL,`has_replay` numeric NOT NULL,`replay_state` text NOT NULL,`replay_size` integer NOT NULL,`replay_sha256` text NOT NULL,`archived_at` datetime,`attempts` integer NOT NULL,`next_attempt_at` datetime,`last_error` text NOT NULL,PRIMARY KEY (`id`),CONSTRAINT `fk_scores_player` FOREIGN KEY (`player_id`) REFERENCES `players`(`id`) ON DELETE CASCADE,CONSTRAINT `fk_scores_leaderboard` FOREIGN KEY (`leaderboard_id`) REFERENCES `leaderboards`(`id`) ON DELETE RESTRICT);
INSERT INTO scores VALUES(5001,'76561198038925092',1001,3,900,900,0.9,300.0,'',1,0,0,500,'Quest 3',1,'2026-10-09T10:00:00Z',1,'archived',2000000,'abc','2026-10-09T10:05:00Z',0,NULL,'');
INSERT INTO scores VALUES(5002,'76561198038925092',1001,9,800,800,0.8,250.0,'',0,2,1,200,'Quest 3',0,'2026-10-08T10:00:00Z',1,'pending',0,'',NULL,1,'2026-10-10T10:00:00Z','timeout');
INSERT INTO scores VALUES(5003,'2169974796454690',1002,1,850,850,0.85,0.0,'GN',1,0,0,400,'Index',1,'2026-10-07T10:00:00Z',0,'none',0,'',NULL,0,NULL,'');
INSERT INTO scores VALUES(5004,'111',1002,4,700,700,0.7,0.0,'',0,5,3,100,'Index',1,'2026-10-06T10:00:00Z',1,'gone',0,'',NULL,0,NULL,'replay no longer available');
CREATE TABLE `users` (`id` integer PRIMARY KEY AUTOINCREMENT,`username` text NOT NULL,`password_hash` text NOT NULL,`created_at` datetime NOT NULL);
INSERT INTO users VALUES(1,'admin','$argon2id$v=19$m=1024,t=1,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA','2026-10-08T09:00:00Z');
CREATE TABLE `sessions` (`token_hash` text,`user_id` integer NOT NULL,`expires_at` datetime NOT NULL,`created_at` datetime NOT NULL,PRIMARY KEY (`token_hash`),CONSTRAINT `fk_sessions_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`) ON DELETE CASCADE);
INSERT INTO sessions VALUES('0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0',1,'2026-11-07T09:00:00Z','2026-10-08T09:00:00Z');
CREATE TABLE `settings` (`key` text,`value` text NOT NULL,PRIMARY KEY (`key`));
INSERT INTO settings VALUES('poll_interval','10m0s');
CREATE TABLE `sync_events` (`id` integer PRIMARY KEY AUTOINCREMENT,`at` datetime NOT NULL,`level` text NOT NULL,`kind` text NOT NULL,`player_id` text,`score_id` integer,`message` text NOT NULL);
INSERT INTO sync_events VALUES(1,'2026-10-10T09:00:00Z','info','replay','76561198038925092',5001,'archived replay (1.9 MB)');
CREATE INDEX `idx_players_backfill_state` ON `players`(`backfill_state`);
CREATE INDEX `idx_leaderboards_song_name` ON `leaderboards`(`song_name`);
CREATE INDEX `idx_leaderboards_song_hash` ON `leaderboards`(`song_hash`);
CREATE INDEX `idx_scores_state_set` ON `scores`(`replay_state`,`set_at` desc);
CREATE INDEX `idx_scores_leaderboard_id` ON `scores`(`leaderboard_id`);
CREATE INDEX `idx_scores_player_set` ON `scores`(`player_id`,`set_at` desc);
CREATE UNIQUE INDEX `idx_users_username` ON `users`(`username`);
CREATE INDEX `idx_sessions_expires_at` ON `sessions`(`expires_at`);
CREATE INDEX `idx_sessions_user_id` ON `sessions`(`user_id`);
CREATE INDEX `idx_sync_events_player_id` ON `sync_events`(`player_id`);
CREATE INDEX `idx_sync_events_kind` ON `sync_events`(`kind`);
CREATE INDEX `idx_sync_events_level` ON `sync_events`(`level`);
CREATE INDEX `idx_sync_events_at` ON `sync_events`(`at`);

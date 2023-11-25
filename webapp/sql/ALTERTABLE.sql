ALTER TABLE `isupipe`.`livestream_tags` ADD INDEX `livestream_id` (`livestream_id`);
ALTER TABLE `isupipe`.`icons` ADD INDEX `user_id` (`user_id`);
ALTER TABLE `isupipe`.`livecomments` ADD INDEX `livestream_id` (`livestream_id`);
ALTER TABLE `isupipe`.`ng_words` ADD INDEX `user_id_livestream_id` (`user_id`, `livestream_id`);
ALTER TABLE `isudns`.`records` ADD INDEX (name);




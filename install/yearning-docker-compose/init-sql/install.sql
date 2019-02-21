/*
 Navicat Premium Data Transfer

 Source Server         : Yearning
 Source Server Type    : MySQL
 Source Server Version : 50721
 Source Host           : 127.0.0.1:3306
 Source Schema         : Yearning

 Target Server Type    : MySQL
 Target Server Version : 50721
 File Encoding         : 65001

 Date: 12/09/2018 17:30:24
*/

SET NAMES utf8mb4;
SET FOREIGN_KEY_CHECKS = 0;

-- ----------------------------
-- Table structure for auth_group
-- ----------------------------
DROP TABLE IF EXISTS `auth_group`;
CREATE TABLE `auth_group` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `name` varchar(80) COLLATE utf8_bin NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `name` (`name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8 COLLATE=utf8_bin;
-- ----------------------------
-- Table structure for auth_group_permissions
-- ----------------------------
DROP TABLE IF EXISTS `auth_group_permissions`;
CREATE TABLE `auth_group_permissions` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `group_id` int(11) NOT NULL,
  `permission_id` int(11) NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `auth_group_permissions_group_id_permission_id_0cd325b0_uniq` (`group_id`,`permission_id`),
  KEY `auth_group_permissio_permission_id_84c5c92e_fk_auth_perm` (`permission_id`),
  CONSTRAINT `auth_group_permissio_permission_id_84c5c92e_fk_auth_perm` FOREIGN KEY (`permission_id`) REFERENCES `auth_permission` (`id`),
  CONSTRAINT `auth_group_permissions_group_id_b120cbf9_fk_auth_group_id` FOREIGN KEY (`group_id`) REFERENCES `auth_group` (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8 COLLATE=utf8_bin;

-- ----------------------------
-- Table structure for auth_permission
-- ----------------------------
DROP TABLE IF EXISTS `auth_permission`;
CREATE TABLE `auth_permission` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `name` varchar(255) COLLATE utf8_bin NOT NULL,
  `content_type_id` int(11) NOT NULL,
  `codename` varchar(100) COLLATE utf8_bin NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `auth_permission_content_type_id_codename_01ab375a_uniq` (`content_type_id`,`codename`),
  CONSTRAINT `auth_permission_content_type_id_2f476e4b_fk_django_co` FOREIGN KEY (`content_type_id`) REFERENCES `django_content_type` (`id`)
) ENGINE=InnoDB AUTO_INCREMENT=55 DEFAULT CHARSET=utf8 COLLATE=utf8_bin;

-- ----------------------------
-- Table structure for core_account
-- ----------------------------
DROP TABLE IF EXISTS `core_account`;
CREATE TABLE `core_account` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `password` varchar(128) COLLATE utf8_bin NOT NULL,
  `last_login` datetime(6) DEFAULT NULL,
  `is_superuser` tinyint(1) NOT NULL,
  `username` varchar(150) COLLATE utf8_bin NOT NULL,
  `first_name` varchar(30) COLLATE utf8_bin NOT NULL,
  `last_name` varchar(150) COLLATE utf8_bin NOT NULL,
  `email` varchar(254) COLLATE utf8_bin NOT NULL,
  `is_staff` tinyint(1) NOT NULL,
  `is_active` tinyint(1) NOT NULL,
  `date_joined` datetime(6) NOT NULL,
  `group` varchar(40) COLLATE utf8_bin NOT NULL,
  `department` varchar(40) COLLATE utf8_bin NOT NULL,
  `auth_group` longtext COLLATE utf8_bin DEFAULT NULL,
  `real_name` varchar(100) COLLATE utf8_bin DEFAULT '请添加真实姓名' NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `username` (`username`)
) ENGINE=InnoDB AUTO_INCREMENT=47 DEFAULT CHARSET=utf8 COLLATE=utf8_bin;

-- ----------------------------
-- Table structure for core_account_groups
-- ----------------------------
DROP TABLE IF EXISTS `core_account_groups`;
CREATE TABLE `core_account_groups` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `account_id` int(11) NOT NULL,
  `group_id` int(11) NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `core_account_groups_account_id_group_id_9e3c433f_uniq` (`account_id`,`group_id`),
  KEY `core_account_groups_group_id_ffac212f_fk_auth_group_id` (`group_id`),
  CONSTRAINT `core_account_groups_account_id_3bd74ec9_fk_core_account_id` FOREIGN KEY (`account_id`) REFERENCES `core_account` (`id`),
  CONSTRAINT `core_account_groups_group_id_ffac212f_fk_auth_group_id` FOREIGN KEY (`group_id`) REFERENCES `auth_group` (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8 COLLATE=utf8_bin;

-- ----------------------------
-- Table structure for core_account_user_permissions
-- ----------------------------
DROP TABLE IF EXISTS `core_account_user_permissions`;
CREATE TABLE `core_account_user_permissions` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `account_id` int(11) NOT NULL,
  `permission_id` int(11) NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `core_account_user_permis_account_id_permission_id_5d993b63_uniq` (`account_id`,`permission_id`),
  KEY `core_account_user_pe_permission_id_6e106098_fk_auth_perm` (`permission_id`),
  CONSTRAINT `core_account_user_pe_account_id_9fe697ec_fk_core_acco` FOREIGN KEY (`account_id`) REFERENCES `core_account` (`id`),
  CONSTRAINT `core_account_user_pe_permission_id_6e106098_fk_auth_perm` FOREIGN KEY (`permission_id`) REFERENCES `auth_permission` (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8 COLLATE=utf8_bin;

-- ----------------------------
-- Table structure for core_applygrained
-- ----------------------------
DROP TABLE IF EXISTS `core_applygrained`;
CREATE TABLE `core_applygrained` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `username` varchar(50) COLLATE utf8_bin NOT NULL,
  `permissions` longtext COLLATE utf8_bin NOT NULL,
  `work_id` varchar(50) COLLATE utf8_bin DEFAULT NULL,
  `status` int(11) DEFAULT NULL,
  `auth_group` varchar(50) COLLATE utf8_bin DEFAULT NULL,
  `real_name` varchar(100) COLLATE utf8_bin DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `core_applygrained_username_01d55fc9` (`username`)
) ENGINE=InnoDB AUTO_INCREMENT=25 DEFAULT CHARSET=utf8 COLLATE=utf8_bin;

-- ----------------------------
-- Table structure for core_databaselist
-- ----------------------------
DROP TABLE IF EXISTS `core_databaselist`;
CREATE TABLE `core_databaselist` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `connection_name` varchar(50) COLLATE utf8_bin NOT NULL,
  `computer_room` varchar(50) COLLATE utf8_bin NOT NULL,
  `ip` varchar(100) COLLATE utf8_bin NOT NULL,
  `username` varchar(150) COLLATE utf8_bin NOT NULL,
  `port` int(11) NOT NULL,
  `password` varchar(500) COLLATE utf8_bin NOT NULL,
  `before` longtext COLLATE utf8_bin,
  `after` longtext COLLATE utf8_bin,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB AUTO_INCREMENT=14 DEFAULT CHARSET=utf8 COLLATE=utf8_bin;

-- ----------------------------
-- Table structure for core_globalpermissions
-- ----------------------------
DROP TABLE IF EXISTS `core_globalpermissions`;
CREATE TABLE `core_globalpermissions` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `authorization` varchar(50) COLLATE utf8_bin DEFAULT NULL,
  `inception` longtext COLLATE utf8_bin,
  `ldap` longtext COLLATE utf8_bin,
  `message` longtext COLLATE utf8_bin,
  `other` longtext COLLATE utf8_bin,
  PRIMARY KEY (`id`),
  KEY `core_globalpermissions_authorization_b3bfe975` (`authorization`)
) ENGINE=InnoDB AUTO_INCREMENT=3 DEFAULT CHARSET=utf8 COLLATE=utf8_bin;

-- ----------------------------
-- Table structure for core_grained
-- ----------------------------
DROP TABLE IF EXISTS `core_grained`;
CREATE TABLE `core_grained` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `username` varchar(50) COLLATE utf8_bin NOT NULL,
  `permissions` longtext COLLATE utf8_bin NOT NULL,
  PRIMARY KEY (`id`),
  KEY `core_grained_username_4cd48d82` (`username`)
) ENGINE=InnoDB AUTO_INCREMENT=36 DEFAULT CHARSET=utf8 COLLATE=utf8_bin;

-- ----------------------------
-- Table structure for core_query_order
-- ----------------------------
DROP TABLE IF EXISTS `core_query_order`;
CREATE TABLE `core_query_order` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `work_id` varchar(50) COLLATE utf8_bin DEFAULT NULL,
  `username` varchar(100) COLLATE utf8_bin DEFAULT NULL,
  `date` varchar(50) COLLATE utf8_bin NOT NULL,
  `instructions` longtext COLLATE utf8_bin,
  `query_per` smallint(6),
  `computer_room` varchar(50) COLLATE utf8_bin DEFAULT NULL,
  `connection_name` varchar(50) COLLATE utf8_bin DEFAULT NULL,
  `export` smallint(6),
  `audit` varchar(100) COLLATE utf8_bin DEFAULT NULL,
  `time` varchar(100) COLLATE utf8_bin DEFAULT NULL,
  `real_name` varchar(100) COLLATE utf8_bin DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `core_query_order_work_id_1ae60daa` (`work_id`)
) ENGINE=InnoDB AUTO_INCREMENT=86 DEFAULT CHARSET=utf8 COLLATE=utf8_bin;

-- ----------------------------
-- Table structure for core_querypermissions
-- ----------------------------
DROP TABLE IF EXISTS `core_querypermissions`;
CREATE TABLE `core_querypermissions` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `work_id` varchar(50) COLLATE utf8_bin DEFAULT NULL,
  `username` varchar(100) COLLATE utf8_bin DEFAULT NULL,
  `statements` longtext COLLATE utf8_bin NOT NULL,
  PRIMARY KEY (`id`),
  KEY `core_querypermissions_work_id_da29a27b` (`work_id`)
) ENGINE=InnoDB AUTO_INCREMENT=85 DEFAULT CHARSET=utf8 COLLATE=utf8_bin;

-- ----------------------------
-- Table structure for core_sqlorder
-- ----------------------------
DROP TABLE IF EXISTS `core_sqlorder`;
CREATE TABLE `core_sqlorder` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `work_id` varchar(50) COLLATE utf8_bin NOT NULL,
  `username` varchar(50) COLLATE utf8_bin NOT NULL,
  `status` int(11) NOT NULL,
  `type` smallint(6) NOT NULL,
  `backup` smallint(6) NOT NULL,
  `bundle_id` int(11) DEFAULT NULL,
  `date` varchar(100) COLLATE utf8_bin NOT NULL,
  `basename` varchar(50) COLLATE utf8_bin NOT NULL,
  `sql` longtext COLLATE utf8_bin NOT NULL,
  `text` longtext COLLATE utf8_bin NOT NULL,
  `assigned` varchar(50) COLLATE utf8_bin NOT NULL,
  `delay` varchar(100) COLLATE utf8_bin DEFAULT NULL,
  `rejected` longtext COLLATE utf8_bin NOT NULL,
  `real_name` varchar(100) COLLATE utf8_bin DEFAULT NULL,
  `executor` varchar(50) COLLATE utf8_bin DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `core_sqlorder_bundle_id_3d5581f1` (`bundle_id`)
) ENGINE=InnoDB AUTO_INCREMENT=78 DEFAULT CHARSET=utf8 COLLATE=utf8_bin;

-- ----------------------------
-- Table structure for core_sqlrecord
-- ----------------------------
DROP TABLE IF EXISTS `core_sqlrecord`;
CREATE TABLE `core_sqlrecord` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `state` varchar(100) COLLATE utf8_bin NOT NULL,
  `sql` longtext COLLATE utf8_bin NOT NULL,
  `error` longtext COLLATE utf8_bin,
  `workid` varchar(50) COLLATE utf8_bin DEFAULT NULL,
  `affectrow` varchar(100) COLLATE utf8_bin DEFAULT NULL,
  `sequence` varchar(50) COLLATE utf8_bin DEFAULT NULL,
  `SQLSHA1` longtext COLLATE utf8_bin,
  `execute_time` varchar(150) COLLATE utf8_bin DEFAULT NULL,
  `backup_dbname` varchar(100) COLLATE utf8_bin DEFAULT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB AUTO_INCREMENT=36 DEFAULT CHARSET=utf8 COLLATE=utf8_bin;

-- ----------------------------
-- Table structure for core_todolist
-- ----------------------------
DROP TABLE IF EXISTS `core_todolist`;
CREATE TABLE `core_todolist` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `username` varchar(50) COLLATE utf8_bin NOT NULL,
  `content` varchar(200) COLLATE utf8_bin NOT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8 COLLATE=utf8_bin;

-- ----------------------------
-- Table structure for django_content_type
-- ----------------------------
DROP TABLE IF EXISTS `django_content_type`;
CREATE TABLE `django_content_type` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `app_label` varchar(100) COLLATE utf8_bin NOT NULL,
  `model` varchar(100) COLLATE utf8_bin NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `django_content_type_app_label_model_76bd3d3b_uniq` (`app_label`,`model`)
) ENGINE=InnoDB AUTO_INCREMENT=19 DEFAULT CHARSET=utf8 COLLATE=utf8_bin;

-- ----------------------------
-- Table structure for django_migrations
-- ----------------------------
DROP TABLE IF EXISTS `django_migrations`;
CREATE TABLE `django_migrations` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `app` varchar(255) COLLATE utf8_bin NOT NULL,
  `name` varchar(255) COLLATE utf8_bin NOT NULL,
  `applied` datetime(6) NOT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB AUTO_INCREMENT=55 DEFAULT CHARSET=utf8 COLLATE=utf8_bin;



BEGIN;
INSERT INTO `core_grained` VALUES (29, 'admin', '{\'ddl\': \'0\', \'ddlcon\': [], \'dml\': \'0\', \'dmlcon\': [], \'dic\': \'0\', \'diccon\': [], \'dicedit\': \'0\', \'user\': \'1\', \'base\': \'1\', \'dicexport\': \'0\', \'person\': [], \'query\': \'0\', \'querycon\': []}');
INSERT INTO `core_globalpermissions` VALUES (2, 'global', '{\'host\': \'\', \'port\': \'\', \'user\': \'\', \'password\': \'\', \'back_host\': \'\', \'back_port\': \'\', \'back_user\': \'\', \'back_password\': \'\'}', '{\'type\': \'1\', \'host\': \'\', \'sc\': \'\', \'domain\': \'\', \'user\': \'\', \'password\': \'\'}', '{\'webhook\': \'\', \'smtp_host\': \'\', \'smtp_port\': \'\', \'user\': \'\', \'password\': \'\', \'to_user\': \'\', \'mail\': False, \'ding\': False, \'ssl\': False}', '{\'limit\': \'\', \'con_room\': [\'AWS\', \'Aliyun\', \'Own\', \'Other\'], \'foce\': \'\', \'multi\': False, \'query\': False, \'sensitive_list\': [], \'sensitive\': \'\', \'exclued_db_list\': [], \'exclued_db\': \'\', \'email_suffix_list\': [], \'email_suffix\': \'\'}');
INSERT INTO `core_account` VALUES (1, 'pbkdf2_sha256$100000$Dy6mFniGxTZa$YBQ9cX0iPQvTYp06C5ZiVgXICTHNTiwWhWYnRmcqjHY=', NULL, 0, 'admin', '', '', '', 1, 1, '2018-07-26 07:15:33.931971', 'admin', '', 'admin', '');
COMMIT;

SET FOREIGN_KEY_CHECKS = 1;

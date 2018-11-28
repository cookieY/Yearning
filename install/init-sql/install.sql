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

use Yearning;

BEGIN;
INSERT INTO `core_grained` VALUES (29, 'admin', '{\'ddl\': \'0\', \'ddlcon\': [], \'dml\': \'0\', \'dmlcon\': [], \'dic\': \'0\', \'diccon\': [], \'dicedit\': \'0\', \'user\': \'1\', \'base\': \'1\', \'dicexport\': \'0\', \'person\': [], \'query\': \'0\', \'querycon\': []}');
INSERT INTO `core_globalpermissions` VALUES (2, 'global', '{\'host\': \'\', \'port\': \'\', \'user\': \'\', \'password\': \'\', \'back_host\': \'\', \'back_port\': \'\', \'back_user\': \'\', \'back_password\': \'\'}', '{\'type\': \'1\', \'host\': \'\', \'sc\': \'\', \'domain\': \'\', \'user\': \'\', \'password\': \'\'}', '{\'webhook\': \'\', \'smtp_host\': \'\', \'smtp_port\': \'\', \'user\': \'\', \'password\': \'\', \'to_user\': \'\', \'mail\': False, \'ding\': False, \'ssl\': False}', '{\'limit\': \'\', \'con_room\': [\'AWS\', \'Aliyun\', \'Own\', \'Other\'], \'foce\': \'\', \'multi\': False, \'query\': False, \'sensitive_list\': [], \'sensitive\': \'\', \'exclued_db_list\': [], \'exclued_db\': \'\', \'email_suffix_list\': [], \'email_suffix\': \'\'}');
INSERT INTO `core_account` VALUES (1, 'pbkdf2_sha256$100000$Dy6mFniGxTZa$YBQ9cX0iPQvTYp06C5ZiVgXICTHNTiwWhWYnRmcqjHY=', NULL, 0, 'admin', '', '', '', 1, 1, '2018-07-26 07:15:33.931971', 'admin', '', 'admin', '');
COMMIT;

SET FOREIGN_KEY_CHECKS = 1;

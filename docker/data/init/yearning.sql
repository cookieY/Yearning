-- MySQL dump 10.13  Distrib 5.7.38, for Linux (x86_64)
--
-- Host: localhost    Database: yearning
-- ------------------------------------------------------
-- Server version	5.7.38

/*!40101 SET @OLD_CHARACTER_SET_CLIENT=@@CHARACTER_SET_CLIENT */;
/*!40101 SET @OLD_CHARACTER_SET_RESULTS=@@CHARACTER_SET_RESULTS */;
/*!40101 SET @OLD_COLLATION_CONNECTION=@@COLLATION_CONNECTION */;
/*!40101 SET NAMES utf8 */;
/*!40103 SET @OLD_TIME_ZONE=@@TIME_ZONE */;
/*!40103 SET TIME_ZONE='+00:00' */;
/*!40014 SET @OLD_UNIQUE_CHECKS=@@UNIQUE_CHECKS, UNIQUE_CHECKS=0 */;
/*!40014 SET @OLD_FOREIGN_KEY_CHECKS=@@FOREIGN_KEY_CHECKS, FOREIGN_KEY_CHECKS=0 */;
/*!40101 SET @OLD_SQL_MODE=@@SQL_MODE, SQL_MODE='NO_AUTO_VALUE_ON_ZERO' */;
/*!40111 SET @OLD_SQL_NOTES=@@SQL_NOTES, SQL_NOTES=0 */;

-- create database yearning character set utf8mb4 collate utf8mb4_general_ci;

use yearning;
SET NAMES utf8mb4;
SET FOREIGN_KEY_CHECKS = 0;

--
-- Table structure for table `core_accounts`
--
DROP TABLE IF EXISTS `core_accounts`;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!40101 SET character_set_client = utf8 */;
CREATE TABLE `core_accounts` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `username` varchar(50) NOT NULL,
  `password` varchar(150) NOT NULL,
  `department` varchar(50) DEFAULT NULL,
  `real_name` varchar(50) DEFAULT NULL,
  `email` varchar(50) DEFAULT NULL,
  `is_recorder` tinyint(2) NOT NULL DEFAULT '2',
  PRIMARY KEY (`id`),
  KEY `user_idx` (`username`)
) ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4;
/*!40101 SET character_set_client = @saved_cs_client */;

--
-- Dumping data for table `core_accounts`
--

LOCK TABLES `core_accounts` WRITE;
/*!40000 ALTER TABLE `core_accounts` DISABLE KEYS */;
INSERT INTO `core_accounts` VALUES (1,'admin','pbkdf2_sha256$120000$bH38SUw0BFkb$bgdAW2tG6AxBZ9hhdF+ZzZIDWzCteRHLoYhbQsKGJ1w=','DBA','超级管理员','',0);
/*!40000 ALTER TABLE `core_accounts` ENABLE KEYS */;
UNLOCK TABLES;

--
-- Table structure for table `core_auto_tasks`
--

DROP TABLE IF EXISTS `core_auto_tasks`;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!40101 SET character_set_client = utf8 */;
CREATE TABLE `core_auto_tasks` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(50) NOT NULL,
  `source` varchar(50) NOT NULL,
  `source_id` varchar(200) NOT NULL,
  `data_base` varchar(50) NOT NULL,
  `table` varchar(50) NOT NULL,
  `tp` tinyint(2) NOT NULL,
  `affectrow` int(50) DEFAULT NULL,
  `status` tinyint(2) DEFAULT NULL,
  `task_id` varchar(200) NOT NULL,
  `id_c` varchar(50) NOT NULL,
  PRIMARY KEY (`id`),
  KEY `source_idx` (`source_id`),
  KEY `task_idx` (`task_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
/*!40101 SET character_set_client = @saved_cs_client */;

--
-- Dumping data for table `core_auto_tasks`
--

LOCK TABLES `core_auto_tasks` WRITE;
/*!40000 ALTER TABLE `core_auto_tasks` DISABLE KEYS */;
/*!40000 ALTER TABLE `core_auto_tasks` ENABLE KEYS */;
UNLOCK TABLES;

--
-- Table structure for table `core_data_sources`
--

DROP TABLE IF EXISTS `core_data_sources`;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!40101 SET character_set_client = utf8 */;
CREATE TABLE `core_data_sources` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `id_c` varchar(50) NOT NULL,
  `source` varchar(50) NOT NULL,
  `ip` varchar(200) NOT NULL,
  `port` int(10) NOT NULL,
  `username` varchar(50) NOT NULL,
  `password` varchar(150) NOT NULL,
  `is_query` tinyint(2) NOT NULL,
  `flow_id` int(100) NOT NULL,
  `source_id` varchar(200) NOT NULL,
  `exclude_db_list` varchar(200) NOT NULL,
  `insulate_word_list` varchar(200) NOT NULL,
  `principal` varchar(150) NOT NULL,
  PRIMARY KEY (`id`),
  KEY `source_idx` (`source_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
/*!40101 SET character_set_client = @saved_cs_client */;

--
-- Dumping data for table `core_data_sources`
--

LOCK TABLES `core_data_sources` WRITE;
/*!40000 ALTER TABLE `core_data_sources` DISABLE KEYS */;
/*!40000 ALTER TABLE `core_data_sources` ENABLE KEYS */;
UNLOCK TABLES;

--
-- Table structure for table `core_global_configurations`
--

DROP TABLE IF EXISTS `core_global_configurations`;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!40101 SET character_set_client = utf8 */;
CREATE TABLE `core_global_configurations` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `authorization` varchar(50) NOT NULL,
  `ldap` json DEFAULT NULL,
  `message` json DEFAULT NULL,
  `other` json DEFAULT NULL,
  `stmt` tinyint(2) NOT NULL DEFAULT '0',
  `audit_role` json DEFAULT NULL,
  `board` longtext,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4;
/*!40101 SET character_set_client = @saved_cs_client */;

--
-- Dumping data for table `core_global_configurations`
--

LOCK TABLES `core_global_configurations` WRITE;
/*!40000 ALTER TABLE `core_global_configurations` DISABLE KEYS */;
INSERT INTO `core_global_configurations` VALUES (1,'global','{\"sc\": \"\", \"map\": \"\", \"url\": \"\", \"type\": \"(&(objectClass=organizationalPerson)(sAMAccountName=%s))\", \"user\": \"\", \"ldaps\": false, \"password\": \"\", \"test_user\": \"\", \"test_password\": \"\"}','{\"key\": \"\", \"ssl\": false, \"ding\": false, \"host\": \"\", \"mail\": false, \"port\": 25, \"user\": \"\", \"to_user\": \"\", \"password\": \"\", \"web_hook\": \"\", \"push_type\": false}','{\"idc\": [\"Aliyun\", \"AWS\"], \"limit\": 1000, \"query\": false, \"export\": false, \"register\": false, \"ex_query_time\": 60}',0,'{\"IsOSC\": false, \"OSCExpr\": \"\", \"OscSize\": 0, \"DMLOrder\": false, \"DMLWhere\": false, \"DDLMaxKey\": 5, \"DMLSelect\": false, \"PRIRollBack\": false, \"MaxAffectRows\": 1000, \"DDLMaxKeyParts\": 5, \"DDLTablePrefix\": \"\", \"SupportCharset\": \"\", \"AllowCreateView\": false, \"CheckIdentifier\": false, \"MaxTableNameLen\": 10, \"MustHaveColumns\": \"\", \"AllowSpecialType\": false, \"DDLIndexNameSpec\": false, \"DDLMaxCharLength\": 10, \"DDLMultiToCommit\": false, \"DMLInsertColumns\": false, \"DMLMaxInsertRows\": 10, \"MaxDDLAffectRows\": 0, \"SupportCollation\": \"\", \"DDLAllowPRINotInt\": false, \"DDLPrimaryKeyMust\": false, \"DMLAllowLimitSTMT\": false, \"DDLAllowColumnType\": false, \"DDLAllowMultiAlter\": false, \"DDLEnableDropTable\": false, \"DMLAllowInsertNull\": false, \"DDLCheckFloatDouble\": false, \"DDLEnableForeignKey\": false, \"DDLEnablePrimaryKey\": false, \"AllowCreatePartition\": false, \"DDLCheckTableComment\": false, \"DDLCheckColumnDefault\": false, \"DDLEnableDropDatabase\": false, \"DDlCheckColumnComment\": false, \"DDLCheckColumnNullable\": false, \"DDLEnableAutoIncrement\": false, \"DDLEnableNullIndexName\": false, \"DDLColumnsMustHaveIndex\": \"\", \"DDLEnableAcrossDBRename\": false, \"DMLInsertMustExplicitly\": false, \"DDLImplicitTypeConversion\": false, \"DDLEnableAutoincrementInit\": false, \"AllowCrateViewWithSelectStar\": false, \"DDLAllowChangeColumnPosition\": false, \"DDLEnableAutoincrementUnsigned\": false}','');
/*!40000 ALTER TABLE `core_global_configurations` ENABLE KEYS */;
UNLOCK TABLES;

--
-- Table structure for table `core_graineds`
--

DROP TABLE IF EXISTS `core_graineds`;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!40101 SET character_set_client = utf8 */;
CREATE TABLE `core_graineds` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `username` varchar(50) NOT NULL,
  `group` json DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `user_idx` (`username`)
) ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4;
/*!40101 SET character_set_client = @saved_cs_client */;

--
-- Dumping data for table `core_graineds`
--

LOCK TABLES `core_graineds` WRITE;
/*!40000 ALTER TABLE `core_graineds` DISABLE KEYS */;
INSERT INTO `core_graineds` VALUES (1,'admin','[\"admin\"]');
/*!40000 ALTER TABLE `core_graineds` ENABLE KEYS */;
UNLOCK TABLES;

--
-- Table structure for table `core_query_orders`
--

DROP TABLE IF EXISTS `core_query_orders`;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!40101 SET character_set_client = utf8 */;
CREATE TABLE `core_query_orders` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `work_id` varchar(50) NOT NULL,
  `username` varchar(50) NOT NULL,
  `date` varchar(50) NOT NULL,
  `approval_time` varchar(50) NOT NULL,
  `text` longtext NOT NULL,
  `assigned` varchar(50) NOT NULL,
  `real_name` varchar(50) NOT NULL,
  `export` tinyint(2) NOT NULL,
  `source_id` varchar(200) NOT NULL,
  `status` tinyint(2) NOT NULL,
  PRIMARY KEY (`id`),
  KEY `workId_idx` (`work_id`),
  KEY `source_idx` (`source_id`),
  KEY `status_idx` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
/*!40101 SET character_set_client = @saved_cs_client */;

--
-- Dumping data for table `core_query_orders`
--

LOCK TABLES `core_query_orders` WRITE;
/*!40000 ALTER TABLE `core_query_orders` DISABLE KEYS */;
/*!40000 ALTER TABLE `core_query_orders` ENABLE KEYS */;
UNLOCK TABLES;

--
-- Table structure for table `core_query_records`
--

DROP TABLE IF EXISTS `core_query_records`;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!40101 SET character_set_client = utf8 */;
CREATE TABLE `core_query_records` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `work_id` varchar(50) NOT NULL,
  `sql` longtext NOT NULL,
  `ex_time` int(10) NOT NULL,
  `time` varchar(50) NOT NULL,
  `source` varchar(50) NOT NULL,
  `schema` varchar(50) NOT NULL,
  PRIMARY KEY (`id`),
  KEY `workId_idx` (`work_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
/*!40101 SET character_set_client = @saved_cs_client */;

--
-- Dumping data for table `core_query_records`
--

LOCK TABLES `core_query_records` WRITE;
/*!40000 ALTER TABLE `core_query_records` DISABLE KEYS */;
/*!40000 ALTER TABLE `core_query_records` ENABLE KEYS */;
UNLOCK TABLES;

--
-- Table structure for table `core_role_groups`
--

DROP TABLE IF EXISTS `core_role_groups`;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!40101 SET character_set_client = utf8 */;
CREATE TABLE `core_role_groups` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(50) NOT NULL,
  `permissions` json DEFAULT NULL,
  `group_id` varchar(200) NOT NULL,
  PRIMARY KEY (`id`),
  KEY `group_idx` (`group_id`)
) ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4;
/*!40101 SET character_set_client = @saved_cs_client */;

--
-- Dumping data for table `core_role_groups`
--

LOCK TABLES `core_role_groups` WRITE;
/*!40000 ALTER TABLE `core_role_groups` DISABLE KEYS */;
INSERT INTO `core_role_groups` VALUES (1,'admin','{\"ddl_source\": [], \"dml_source\": [], \"query_source\": []}','');
/*!40000 ALTER TABLE `core_role_groups` ENABLE KEYS */;
UNLOCK TABLES;

--
-- Table structure for table `core_rollbacks`
--

DROP TABLE IF EXISTS `core_rollbacks`;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!40101 SET character_set_client = utf8 */;
CREATE TABLE `core_rollbacks` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `work_id` varchar(50) NOT NULL,
  `sql` longtext NOT NULL,
  PRIMARY KEY (`id`),
  KEY `workId_idx` (`work_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
/*!40101 SET character_set_client = @saved_cs_client */;

--
-- Dumping data for table `core_rollbacks`
--

LOCK TABLES `core_rollbacks` WRITE;
/*!40000 ALTER TABLE `core_rollbacks` DISABLE KEYS */;
/*!40000 ALTER TABLE `core_rollbacks` ENABLE KEYS */;
UNLOCK TABLES;

--
-- Table structure for table `core_sql_orders`
--

DROP TABLE IF EXISTS `core_sql_orders`;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!40101 SET character_set_client = utf8 */;
CREATE TABLE `core_sql_orders` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `work_id` varchar(50) NOT NULL,
  `username` varchar(50) NOT NULL,
  `status` tinyint(2) NOT NULL,
  `type` tinyint(2) NOT NULL,
  `backup` tinyint(2) NOT NULL,
  `id_c` varchar(50) NOT NULL,
  `source` varchar(50) NOT NULL,
  `source_id` varchar(200) NOT NULL,
  `data_base` varchar(50) NOT NULL,
  `table` varchar(50) NOT NULL,
  `date` varchar(50) NOT NULL,
  `sql` longtext NOT NULL,
  `text` longtext NOT NULL,
  `assigned` varchar(550) NOT NULL,
  `delay` varchar(50) NOT NULL DEFAULT 'none',
  `real_name` varchar(50) NOT NULL,
  `execute_time` varchar(50) DEFAULT NULL,
  `time` varchar(50) NOT NULL,
  `current_step` int(50) DEFAULT NULL,
  `relevant` json DEFAULT NULL,
  `osc_info` longtext,
  `file` varchar(200) NOT NULL,
  PRIMARY KEY (`id`),
  KEY `source_idx` (`source_id`),
  KEY `workId_idx` (`work_id`),
  KEY `query_idx` (`username`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
/*!40101 SET character_set_client = @saved_cs_client */;

--
-- Dumping data for table `core_sql_orders`
--

LOCK TABLES `core_sql_orders` WRITE;
/*!40000 ALTER TABLE `core_sql_orders` DISABLE KEYS */;
/*!40000 ALTER TABLE `core_sql_orders` ENABLE KEYS */;
UNLOCK TABLES;

--
-- Table structure for table `core_sql_records`
--

DROP TABLE IF EXISTS `core_sql_records`;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!40101 SET character_set_client = utf8 */;
CREATE TABLE `core_sql_records` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `work_id` varchar(50) NOT NULL,
  `sql` longtext NOT NULL,
  `state` varchar(50) NOT NULL,
  `affectrow` int(50) NOT NULL,
  `time` varchar(50) NOT NULL,
  `error` longtext,
  PRIMARY KEY (`id`),
  KEY `workId_idx` (`work_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
/*!40101 SET character_set_client = @saved_cs_client */;

--
-- Dumping data for table `core_sql_records`
--

LOCK TABLES `core_sql_records` WRITE;
/*!40000 ALTER TABLE `core_sql_records` DISABLE KEYS */;
/*!40000 ALTER TABLE `core_sql_records` ENABLE KEYS */;
UNLOCK TABLES;

--
-- Table structure for table `core_workflow_details`
--

DROP TABLE IF EXISTS `core_workflow_details`;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!40101 SET character_set_client = utf8 */;
CREATE TABLE `core_workflow_details` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `work_id` varchar(50) NOT NULL,
  `username` varchar(50) NOT NULL,
  `time` varchar(50) NOT NULL,
  `action` varchar(550) NOT NULL,
  PRIMARY KEY (`id`),
  KEY `workId_idx` (`work_id`),
  KEY `query_idx` (`username`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
/*!40101 SET character_set_client = @saved_cs_client */;

--
-- Dumping data for table `core_workflow_details`
--

LOCK TABLES `core_workflow_details` WRITE;
/*!40000 ALTER TABLE `core_workflow_details` DISABLE KEYS */;
/*!40000 ALTER TABLE `core_workflow_details` ENABLE KEYS */;
UNLOCK TABLES;

--
-- Table structure for table `core_workflow_tpls`
--

DROP TABLE IF EXISTS `core_workflow_tpls`;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!40101 SET character_set_client = utf8 */;
CREATE TABLE `core_workflow_tpls` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `source` varchar(50) NOT NULL,
  `steps` json DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `source_idx` (`source`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
/*!40101 SET character_set_client = @saved_cs_client */;

--
-- Dumping data for table `core_workflow_tpls`
--

LOCK TABLES `core_workflow_tpls` WRITE;
/*!40000 ALTER TABLE `core_workflow_tpls` DISABLE KEYS */;
/*!40000 ALTER TABLE `core_workflow_tpls` ENABLE KEYS */;
UNLOCK TABLES;
/*!40103 SET TIME_ZONE=@OLD_TIME_ZONE */;

/*!40101 SET SQL_MODE=@OLD_SQL_MODE */;
/*!40014 SET FOREIGN_KEY_CHECKS=@OLD_FOREIGN_KEY_CHECKS */;
/*!40014 SET UNIQUE_CHECKS=@OLD_UNIQUE_CHECKS */;
/*!40101 SET CHARACTER_SET_CLIENT=@OLD_CHARACTER_SET_CLIENT */;
/*!40101 SET CHARACTER_SET_RESULTS=@OLD_CHARACTER_SET_RESULTS */;
/*!40101 SET COLLATION_CONNECTION=@OLD_COLLATION_CONNECTION */;
/*!40111 SET SQL_NOTES=@OLD_SQL_NOTES */;

-- Dump completed on 2022-07-05  9:07:55

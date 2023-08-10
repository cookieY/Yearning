<div align="center">

<h1 style="border-bottom: none">
    <b><a href="https://next.yearning.io">Yearning</a></b><br />
        Simple, Efficient and MYSQL-Like
    <br>
</h1>
<p>
Seamlessly integrates SQL detection and query auditing, tailored for the use of DBAs and developers. <br />
A locally deployed, privacy-focused, simple and efficient for MYSQL audit platform.
</p>
</div>
<div align="center">

![](https://img.shields.io/badge/-x86_x64%20ARM%20Supports%20%E2%86%92-rgb(84,56,255)?style=flat-square&logoColor=white&logo=linux)
[![OSCS Status](https://www.oscs1024.com/platform/badge/cookieY/Yearning.svg?size=small)](https://www.murphysec.com/dr/nDuoncnUbuFMdrZsh7)

![LICENSE](https://img.shields.io/badge/license-AGPL%20-blue.svg)
![](https://img.shields.io/github/languages/top/cookieY/Yearning)
![](https://img.shields.io/docker/image-size/yeelabs/yearning/latest?logo=docker)
<img alt="Github Stars" src="https://img.shields.io/github/stars/cookieY/Yearning?logo=github">
[![Releases](https://img.shields.io/github/downloads/cookieY/Yearning/total)](https://github.com/cookieY/Yearning/releases/latest)
</div>

English | [简体中文](README.zh-CN.md)

## Feature

- **SQL Audit** — Support the creation of SQL audit tickets with approval workflows and automated syntax checkers to
  validate submitted SQL statements for correctness, security, and compliance. Automatically generate rollback
  statements corresponding to the submitted DDL/DML operations for easy recovery when needed. Maintain a comprehensive
  history log of all SQL audit operations for traceability and auditing purposes.
- **Query Audit** — Our solution supports auditing of user query statements, including restrictions on data sources and
  databases, as well as anonymization of sensitive fields. Query records are also saved for future reference.
- **Check Rules** — The automated syntax checker supports dozens of check rules, catering to most of the automatic
  checking scenarios.
- **Privacy focussed** - Locally deployable and open-source solution ensures the security of your database and SQL
  statements. In addition to providing control over the infrastructure, the solution also includes encryption mechanisms
  to protect sensitive data before storing it in your database. This ensures that even if there is unauthorized access
  to the database, the encrypted data remains secure and unreadable. By combining local deployment, open-source
  transparency, and data encryption, we prioritize the privacy and security of your database and SQL statements.
- **RBAC** - In our platform, you can create and manage different roles and assign specific permissions to each role.
  This allows you to restrict users' access to query work orders, auditing functions, and other sensitive operations
  based on their assigned roles.

## Docs

[Yearning Docs](https://next.yearning.io) only Chinese

## Install

[Download](https://github.com/cookieY/Yearning/releases/latest) the latest release and extract it.

**First make sure you have configured ./config.toml**

#### Manual

```bash
## init database
./Yearning install

## start
./Yearning run

## help
./Yearning --help

```

**Yes, it's that simple**

#### Docker

```bash
## init database
docker run --rm -it -p8000:8000 -e SECRET_KEY=$SECRET_KEY -e MYSQL_USER=$MYSQL_USER -e MYSQL_ADDR=$MYSQL_ADDR -e MYSQL_PASSWORD=$MYSQL_PASSWORD -e MYSQL_DB=$Yearning_DB yeelabs/yearning "/opt/Yearning install"
## You must initialize your database in the startup container
docker run -d -it -p8000:8000 -e SECRET_KEY=$SECRET_KEY -e MYSQL_USER=$MYSQL_USER -e MYSQL_ADDR=$MYSQL_ADDR -e MYSQL_PASSWORD=$MYSQL_PASSWORD -e MYSQL_DB=$Yearning_DB yeelabs/yearning
```

## Recommend

[Spug - 开源轻量自动化运维平台](https://github.com/openspug/spug)

<h1 align="center">Automatic SQL Checker</h1>
<p align="center">
The SQL statement detection function tests against predefined rules and syntax <br /> we can set predefined rules to check whether the SQL statement conforms to specific coding standards, best practices or security requirements.
</p>

<img src="img/audit.png" style="width: 1000px" /> 

<br />
<h2 align="center">SQL syntax highlighting and Auto-completion</h2>
<p align="center">
SQL syntax highlighting and auto-completion features to enhance the user experience and improve query writing efficiency.<br />SQL syntax highlighting helps users visually distinguish different parts of the SQL query, such as keywords, table names, column names, and operators.  This makes it easier to read and understand the query structure.
</p>
<img src="img/query.png" style="width: 1000px" />
<br />
<br />
<h2 align="center">Order/Query record</h2>
<p align="center">
  Supports auditing of user order/query statements <br /> Through the auditing feature, you can track and record all query operations, including the data source, database, and handling of sensitive fields. This ensures that query operations comply with regulations and allows for tracing query history.


</p>
<img src="img/record.png" style="width: 1000px" />

<br />

## Ecosystem

[Gemini](https://github.com/cookieY/gemini-next) Yearning front-end Project

[Yee](https://github.com/cookieY/yee) Yearning web framework

## Contact Us

E-mail: henry@yearning.io

## License

See [LICENSE](LICENSE) for details.

2023 © Henry Yee

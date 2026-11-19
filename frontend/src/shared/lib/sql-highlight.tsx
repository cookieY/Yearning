import * as React from "react";

const SQL_KEYWORDS =
  "SELECT|FROM|WHERE|AND|OR|NOT|NULL|TRUE|FALSE|INSERT|INTO|VALUES|UPDATE|SET|DELETE|CREATE|TABLE|ALTER|DROP|INDEX|PRIMARY|KEY|UNIQUE|FOREIGN|REFERENCES|DEFAULT|IF|EXISTS|JOIN|LEFT|RIGHT|INNER|OUTER|CROSS|ON|GROUP|BY|ORDER|HAVING|LIMIT|OFFSET|AS|DISTINCT|UNION|ALL|BEGIN|COMMIT|ROLLBACK|TRUNCATE|VIEW|DATABASE|SCHEMA|GRANT|REVOKE|WITH|RENAME|ADD|COLUMN|CONSTRAINT|CASCADE|CHECK|ENGINE|CHARSET|COLLATE|AUTO_INCREMENT|COMMENT|PARTITION|BETWEEN|LIKE|ILIKE|IN|IS|INTERVAL|CASE|WHEN|THEN|ELSE|END|ASC|DESC|INT|INTEGER|BIGINT|SMALLINT|TINYINT|VARCHAR|CHAR|TEXT|BOOLEAN|DATE|DATETIME|TIMESTAMP|TIMESTAMPTZ|DECIMAL|NUMERIC|FLOAT|DOUBLE|PRECISION|SERIAL|BIGSERIAL|UUID|JSON|JSONB|BYTEA|BLOB|ENUM|COALESCE|NULLIF|GREATEST|LEAST|CAST|COUNT|SUM|AVG|MIN|MAX|LOWER|UPPER|SUBSTRING|TRIM|CONCAT|LENGTH|ROUND|FLOOR|CEIL|ABS|NOW|CURRENT_TIMESTAMP|CURRENT_DATE|EXPLAIN|ANALYZE|VERBOSE|BUFFERS|FORMAT|RETURNING|CONCURRENTLY|USING|ONLY|NULLS|FIRST|LAST|OVER|FILTER|LATERAL|CONFLICT|DO|NOTHING|ANY|SOME|ARRAY|SHOW|DESCRIBE|USE|REPLACE|IGNORE|UNSIGNED|FOR|SHARE|LOCK";

const SQL_TOKEN_PATTERN = new RegExp(
  `('[^']*'|"[^"]*")|(--[^\\n]*)|\\b(${SQL_KEYWORDS})\\b|(\\$\\d+|\\b\\d+(?:\\.\\d+)?\\b)|([(),;=<>*/+\\-:])`,
  "gi",
);

export function highlightSqlLine(line: string): React.ReactNode[] {
  const nodes: React.ReactNode[] = [];
  let last = 0;
  for (const match of line.matchAll(SQL_TOKEN_PATTERN)) {
    const index = match.index;
    if (index > last) nodes.push(line.slice(last, index));
    const [full, string, comment, keyword, number] = match;
    if (string) {
      nodes.push(
        <span key={index} className="code-token-string">
          {string}
        </span>,
      );
    } else if (comment) {
      nodes.push(
        <span key={index} className="code-token-comment">
          {comment}
        </span>,
      );
    } else if (keyword) {
      nodes.push(
        <span key={index} className="code-token-keyword">
          {keyword}
        </span>,
      );
    } else if (number) {
      nodes.push(
        <span key={index} className="code-token-number">
          {number}
        </span>,
      );
    } else {
      nodes.push(
        <span key={index} className="text-muted-foreground">
          {full}
        </span>,
      );
    }
    last = index + full.length;
  }
  if (last < line.length) nodes.push(line.slice(last));
  return nodes;
}

export function highlightSqlText(text: string): React.ReactNode {
  const lines = text.split("\n");
  return lines.map((line, lineIdx) => (
    <React.Fragment key={lineIdx}>
      {lineIdx > 0 ? "\n" : null}
      {highlightSqlLine(line)}
    </React.Fragment>
  ));
}

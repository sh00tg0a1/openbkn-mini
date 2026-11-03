-- Two small related tables for the smoke test, with a read-only account so
-- the Vega catalog only sees this database.
DROP DATABASE IF EXISTS mini_smoke;
CREATE DATABASE mini_smoke CHARACTER SET utf8mb4;
CREATE TABLE mini_smoke.supplier (id VARCHAR(16) PRIMARY KEY, name VARCHAR(64), city VARCHAR(32));
CREATE TABLE mini_smoke.part (id VARCHAR(16) PRIMARY KEY, name VARCHAR(64), supplier_id VARCHAR(16), price DECIMAL(10,2));
INSERT INTO mini_smoke.supplier VALUES ('S1','华东精密','上海'),('S2','北方铸造','天津');
INSERT INTO mini_smoke.part VALUES ('P1','轴承','S1',12.50),('P2','齿轮','S1',30.00),('P3','法兰','S2',8.80);
CREATE USER IF NOT EXISTS 'mini_smoke'@'%' IDENTIFIED BY 'mini_smoke';
GRANT SELECT ON mini_smoke.* TO 'mini_smoke'@'%';

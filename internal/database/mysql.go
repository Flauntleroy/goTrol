package database

import (
	"database/sql"
	"fmt"

	_ "github.com/go-sql-driver/mysql"
	"gotrol/internal/config"
)

type MySQL struct {
	DB *sql.DB
}

func NewMySQL(cfg config.DatabaseConfig) (*MySQL, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&loc=Local",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Name)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &MySQL{DB: db}, nil
}

func (m *MySQL) Close() error {
	return m.DB.Close()
}

func (m *MySQL) GetBPJSCredentials() (*config.BPJSCredentials, error) {
	creds := &config.BPJSCredentials{}

	rows, err := m.DB.Query(`
		SELECT module, field, value 
		FROM mlite_settings 
		WHERE module = 'jkn_mobile'
		AND field IN (
			'BpjsConsID',
			'BpjsSecretKey', 
			'BpjsAntrianUrl',
			'BpjsUserKey',
			'kd_pj_bpjs'
		)
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var module, field, value string
		if err := rows.Scan(&module, &field, &value); err != nil {
			continue
		}
		switch field {
		case "BpjsConsID":
			creds.ConsID = value
		case "BpjsSecretKey":
			creds.SecretKey = value
		case "BpjsAntrianUrl":
			creds.AntrianURL = value
		case "BpjsUserKey":
			creds.UserKey = value
		case "kd_pj_bpjs":
			creds.KdPjBPJS = value
		}
	}

	return creds, nil
}

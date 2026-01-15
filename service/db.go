package service

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

func InitDB(dsn string, ctx context.Context) (*pgxpool.Pool) {

	var err error
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}

	return db
}

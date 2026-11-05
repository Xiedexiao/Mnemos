package repository

import (
	"context"
	"strings"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// indexCheckRepository implements interfaces.IndexCheckRepository
// (KnowAxis V3.0 §17.1 性能自检).
type indexCheckRepository struct {
	db *gorm.DB
}

// NewIndexCheckRepository constructs the repository.
func NewIndexCheckRepository(db *gorm.DB) interfaces.IndexCheckRepository {
	return &indexCheckRepository{db: db}
}

// Dialect returns the database dialect name.
func (r *indexCheckRepository) Dialect() string {
	if r.db == nil || r.db.Dialector == nil {
		return ""
	}
	return r.db.Dialector.Name()
}

// Explain returns the query plan for one check.
//
// 两种方言的前缀不同：SQLite 是 `EXPLAIN QUERY PLAN`，PostgreSQL 是
// `EXPLAIN`。用错前缀在 SQLite 上会返回字节码而不是计划，解析器会把它当成
// "不认识"——那正是 unknown 存在的意义，但这里应当一次做对。
func (r *indexCheckRepository) Explain(
	ctx context.Context, check types.IndexCheckQuery,
) (string, error) {
	prefix := "EXPLAIN "
	if r.Dialect() == "sqlite" {
		prefix = "EXPLAIN QUERY PLAN "
	}
	rows, err := r.db.WithContext(ctx).Raw(prefix+check.SQL, check.Args...).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return "", err
	}
	var lines []string
	for rows.Next() {
		values := make([]interface{}, len(columns))
		pointers := make([]interface{}, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return "", err
		}
		// 只取文本列拼成一行：SQLite 的计划在 detail 列，PostgreSQL 在
		// "QUERY PLAN" 单列。把每一列都渲染出来在两种方言下都成立。
		parts := make([]string, 0, len(values))
		for _, value := range values {
			switch typed := value.(type) {
			case []byte:
				parts = append(parts, string(typed))
			case string:
				parts = append(parts, typed)
			default:
				// 数字列（SQLite 的 id/parent/notused）跳过，它们对判定无用。
				continue
			}
		}
		if len(parts) > 0 {
			lines = append(lines, strings.Join(parts, " | "))
		}
	}
	return strings.Join(lines, "\n"), rows.Err()
}

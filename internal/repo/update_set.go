// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo

import (
	"fmt"
	"strings"
)

// updateSet — аккумулятор column-scoped SET-фрагментов для read-modify-write
// Update. Read-modify-write use-case читает строку (Get), применяет маску к
// СВОЕМУ снимку в памяти и вызывает repo.Update. Если писать весь column-set,
// конкурентный Update по другому полю затирается (lost update): второй writer
// пишет устаревшее значение независимой колонки поверх изменения первого.
//
// updateSet пишет ТОЛЬКО фактически изменённые колонки (`changed`-набор из
// use-case). $1 зарезервирован под id (WHERE id=$1), поэтому placeholder'ы
// колонок начинаются с $2. Имена колонок — хардкод из вызывающего repo (не из
// пользовательского ввода) → SQL-инъекция невозможна.
type updateSet struct {
	cols []string
	args []any
}

// newUpdateSet создаёт аккумулятор; id занимает $1.
func newUpdateSet(id string) *updateSet {
	return &updateSet{args: []any{id}}
}

// add добавляет `col = $N` с соответствующим значением (N назначается по порядку).
func (u *updateSet) add(col string, val any) {
	u.args = append(u.args, val)
	u.cols = append(u.cols, fmt.Sprintf("%s = $%d", col, len(u.args)))
}

// empty — true если ни одна колонка не изменена (mask не задел ни одной
// mutable-колонки; напр. mask=[metadata_options] на Instance).
func (u *updateSet) empty() bool { return len(u.cols) == 0 }

// clause возвращает "SET col1 = $2, col2 = $3" (без ключевого слова UPDATE).
func (u *updateSet) clause() string {
	return "SET " + strings.Join(u.cols, ", ")
}

// changedSet — набор фактически изменённых mask-полей для O(1) проверки.
func changedSet(changed []string) map[string]struct{} {
	m := make(map[string]struct{}, len(changed))
	for _, f := range changed {
		m[f] = struct{}{}
	}
	return m
}

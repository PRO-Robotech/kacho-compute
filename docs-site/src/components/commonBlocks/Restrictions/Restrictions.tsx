import React from 'react'

export type RestrictionItem = { label: string; rules: readonly string[] }

/** Restrictions — таблица «поле → правила валидации» (контракт Kachō). */
export function Restrictions({ items }: { items: readonly RestrictionItem[] }): React.ReactElement {
  return (
    <div className="kv-block">
      <div className="kv-block__title">Ограничения и валидация</div>
      <table>
        <thead>
          <tr>
            <th>Поле</th>
            <th>Правила</th>
          </tr>
        </thead>
        <tbody>
          {items.map((it, i) => (
            <tr key={i}>
              <td>
                <code>{it.label}</code>
              </td>
              <td>
                <ul style={{ margin: 0, paddingLeft: '1.1rem' }}>
                  {it.rules.map((r, j) => (
                    <li key={j}>{r}</li>
                  ))}
                </ul>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

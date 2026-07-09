import React from 'react'

/** StatusTable — таблица значений status-enum ресурса. */
export function StatusTable({ values }: { values: readonly { code: string; desc: string }[] }): React.ReactElement {
  return (
    <table>
      <thead>
        <tr>
          <th>status</th>
          <th>Описание</th>
        </tr>
      </thead>
      <tbody>
        {values.map((v) => (
          <tr key={v.code}>
            <td>
              <code>{v.code}</code>
            </td>
            <td>{v.desc}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

import React from 'react'
import { CODES, type CodeKey } from '@site/src/constants/codes'

/** Codes — таблица gRPC-кодов ошибок, которые может вернуть операция. */
export function Codes({ codes }: { codes: readonly CodeKey[] }): React.ReactElement {
  return (
    <div className="kv-block">
      <div className="kv-block__title">Коды ошибок</div>
      <table>
        <thead>
          <tr>
            <th>gRPC code</th>
            <th>HTTP</th>
            <th>Когда</th>
          </tr>
        </thead>
        <tbody>
          {codes.map((c) => {
            const e = CODES[c]
            return (
              <tr key={c}>
                <td>
                  <code>{e.grpc}</code>
                </td>
                <td>
                  <code>{e.http}</code>
                </td>
                <td>{e.when}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

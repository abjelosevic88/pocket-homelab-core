import { Modal } from './ui'

export function ShortcutsSheet({ onClose, admin }: { onClose: () => void; admin: boolean }) {
  const groups: { title: string; keys: [string[], string][] }[] = [
    { title: 'Go to', keys: [
      [['o'], 'Overview'],
      ...(admin ? [[['a'], 'Apps'], [['s'], 'Services'], [['e'], 'Events'], [['l'], 'Links']] as [string[], string][] : []),
      [['d'], 'Devices'],
      ...(admin ? [[['u'], 'People']] as [string[], string][] : []),
      [['i'], 'AI connections'], [[','], 'Settings'], [['/'], 'Search & commands'], [['⌘', 'K'], 'Search & commands'],
    ] },
    { title: 'Do', keys: [[['p'], 'Pair a device']] },
    { title: 'Window', keys: [[['['], 'Show or hide the sidebar'], [['esc'], 'Close a dialog or menu']] },
    { title: 'Help', keys: [[['?'], 'This sheet']] },
  ]
  return (
    <Modal title="Keyboard shortcuts" onClose={onClose} size="lg">
      <div className="shortcuts">
        {groups.map(g => (
          <section key={g.title}>
            <h4 className="sc-h">{g.title}</h4>
            {g.keys.map(([keys, label]) => (
              <div key={label + keys.join('')} className="sc-row"><span>{label}</span><span className="sc-keys">{keys.map(k => <kbd key={k}>{k}</kbd>)}</span></div>
            ))}
          </section>
        ))}
      </div>
    </Modal>
  )
}

import { Marked } from 'marked'
import DOMPurify from 'dompurify'

const escape = (text: string) => text.replace(/[&<>"']/g, ch => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]!))
const markdown = new Marked({
  gfm: true,
  breaks: false,
  renderer: {
    // Model output is Markdown, never application HTML. Images become links so
    // an answer cannot silently fetch a third-party image with personal data.
    html: token => escape(token.text),
    image: token => `<a href="${escape(token.href)}">${escape(token.text || 'View image')}</a>`,
  },
})

export function render(node: HTMLElement, source: string) {
  try {
    const fragment = DOMPurify.sanitize(markdown.parse(source, { async: false }), {
      RETURN_DOM_FRAGMENT: true,
      ALLOWED_TAGS: ['p', 'br', 'strong', 'em', 'del', 's', 'a', 'code', 'pre', 'blockquote', 'ul', 'ol', 'li', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'hr', 'table', 'thead', 'tbody', 'tr', 'th', 'td', 'input'],
      ALLOWED_ATTR: ['href', 'title', 'align', 'start', 'type', 'checked', 'disabled'],
      ALLOW_DATA_ATTR: false,
      ALLOW_ARIA_ATTR: false,
    })
    for (const link of fragment.querySelectorAll('a')) {
      const href = link.getAttribute('href')
      let safe = false
      try { safe = !!href && ['https:', 'http:', 'mailto:'].includes(new URL(href, location.href).protocol) } catch { /* Keep malformed links as text. */ }
      if (!safe) link.removeAttribute('href')
      else { link.target = '_blank'; link.rel = 'noopener noreferrer' }
    }
    for (const input of fragment.querySelectorAll('input')) {
      input.type = 'checkbox'; input.disabled = true
    }
    for (const table of fragment.querySelectorAll('table')) {
      const scroll = document.createElement('div')
      scroll.className = 'table-scroll'; scroll.tabIndex = 0
      scroll.setAttribute('role', 'region'); scroll.setAttribute('aria-label', 'Table; scroll horizontally for more columns')
      table.before(scroll); scroll.append(table)
      for (const th of table.querySelectorAll('th')) th.scope = 'col'
    }
    for (const pre of fragment.querySelectorAll('pre')) {
      pre.tabIndex = 0; pre.setAttribute('aria-label', 'Code block')
    }
    node.replaceChildren(fragment)
  } catch {
    // Incomplete streamed Markdown and unexpected parser failures must never
    // erase the answer or fall back to unescaped HTML.
    node.textContent = source
  }
}

declare global { interface Window { PocketAskMarkdown: { render: typeof render } } }
window.PocketAskMarkdown = { render }

/** Shared hub/portal footer-column mutators. Callers supply state updates and
 * persistence for `{ title, links: [{text, url}] }` columns. */
export function createFooterLinkHelpers({ setColumns, saveCustomizations }) {
  function addFooterLink(columnIndex) {
    setColumns((columns) =>
      columns.map((col, idx) =>
        idx === columnIndex ? { ...col, links: [...col.links, { text: '', url: '' }] } : col
      )
    );
    saveCustomizations();
  }

  function removeFooterLink(columnIndex, linkIndex) {
    setColumns((columns) =>
      columns.map((col, idx) =>
        idx === columnIndex ? { ...col, links: col.links.filter((_, i) => i !== linkIndex) } : col
      )
    );
    saveCustomizations();
  }

  function updateColumnTitle(columnIndex, title) {
    setColumns((columns) =>
      columns.map((col, idx) => (idx === columnIndex ? { ...col, title } : col))
    );
    saveCustomizations();
  }

  function updateFooterLink(columnIndex, linkIndex, field, value) {
    setColumns((columns) =>
      columns.map((col, idx) =>
        idx === columnIndex
          ? {
              ...col,
              links: col.links.map((link, i) =>
                i === linkIndex ? { ...link, [field]: value } : link
              ),
            }
          : col
      )
    );
    saveCustomizations();
  }

  return { addFooterLink, removeFooterLink, updateColumnTitle, updateFooterLink };
}

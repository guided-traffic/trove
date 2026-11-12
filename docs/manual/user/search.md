# Search

The search field in the top bar finds repositories, packages and projects you may read, and takes you to your usual places. This
page explains what the field offers before and while you type, how to narrow it to one kind, the keys that reach it, and the results
page that Enter opens.

## Opening the field

Click the field, or press `/` anywhere outside a text field, or Ctrl+K (⌘K on a Mac). On a narrow window the field is a search
button in the top bar; it opens the search over the whole width, and **Close** or Escape closes it again.

`/` works while **Keyboard shortcuts with a single key** is on in your [profile](getting-started.md); it is on unless you turn it
off. Ctrl+K and ⌘K work either way. Without signing in, while an installation has public content, the field is there too, with
Ctrl+K and ⌘K.

## Before you type

The opened field lists, in this order:

1. **Places**: Projects, Guides, Profile and Personal tokens.
2. **Frequently visited repositories**: the repositories you opened most often lately, up to five.
3. **View all my projects**.
4. **Frequently visited projects**: up to five.

trove keeps what you visited in your browser, for your sign-in only: another person signing in on the same browser sees none of
your entries, and a second browser starts empty. Only what you may still read is shown; an entry you lost access to is left out.

## While you type

From two characters on, the field asks trove for the best matches of what you may read, a moment after you stop typing, and shows
them in groups: **Repositories**, **Packages**, **Projects**, then **Pages** of trove itself. Each entry shows where it lives, for
example `npm-vendor · web-frontend`, and the text you typed is bold. A group with more matches ends with **All repositories for
"…"** (packages, projects), which opens the results page on that tab.

Use the arrow keys to move through the entries and Enter to open one, or click it. Escape closes the list; a second Escape empties
the field.

If you search very quickly, trove may ask you to wait: the list says **Too many searches** and when suggestions return.

## Narrowing to one kind

The filter button at the end of the field narrows the search to **Repositories**, **Packages** or **Projects**. The chosen kind
shows as a chip at the start of the field; its × or Backspace in an empty field returns to **All**.

## The results page

Enter without choosing an entry opens the results page **Search**, in the area you work in. Its tabs **Repositories**, **Packages**
and **Projects** list everything you may read that matches your text, 25 at a time with **Load more**. A repository's row copies its
client address with the button at its end; a package shows the project and repository that hold it.

## For administrators

An administrator of the installation also sees, last and apart, the pages of the admin area, users and groups, each marked **Admin
area**. Only choosing one of those marked entries opens the admin area; Enter and the results page always stay in the area you work
in. See [the admin dashboard](../admin/dashboard.md).

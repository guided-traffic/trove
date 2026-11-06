# Getting started

trove stores the packages and files your teams publish, caches public npm packages, and serves both under one address per repository. This page shows how to sign in, how the screen is laid out, how to change the language and the theme, and where to go next.

## What trove does for you

trove keeps your packages and files in repositories. You use it in two ways:

- **In the browser**, to find packages and files, read their details, copy install commands and set up your package manager.
- **With your tools**, such as npm, pnpm, Yarn or curl, which install, publish, download and upload through the repository addresses trove shows you.

The browser signs you in through your organization's identity provider. Tools never use your password: they use a token you create in trove. In this version trove serves npm packages and generic files; [Known limitations](../admin/limitations.md) lists what is not built yet.

## Sign in

Open the address of your installation, for example `https://trove.example.com/`. trove sends you to the sign-in screen when you are not signed in and the installation does not allow anonymous access.

### The sign-in screen

The sign-in screen shows only what your administrators configured.

| What you see | What it does |
|---|---|
| **Sign in with** and the name of a provider | Takes you to your organization's identity provider. After you sign in there, trove returns you to the page you wanted. There is one button per configured provider |
| **Currently unavailable** next to a provider | trove cannot reach that provider right now. Try again later or use another provider |
| **Redirecting to the identity provider.** | trove is sending you to the provider, either after your choice or because the installation sends everyone straight there |
| **Continue without signing in** | Opens trove anonymously. Shown only where anonymous access is allowed |
| **Local administrator** with **Username** and **Password** | The form of the emergency administrator account. It is not for regular users, who never have a password in trove |
| **No sign-in method is configured.** | Ask your administrator |

### Continue without signing in

Where your administrators allow anonymous access, you can use trove without an account: either through **Continue without signing in** on the sign-in screen, or directly, because trove then opens without asking you to sign in. You see only the repositories, packages and files that are open to anonymous readers, and you cannot upload, publish or delete anything. The top bar shows **Sign in** while you are anonymous.

### When your session ends

A browser session ends after a period without activity and after a maximum lifetime that your administrators set (by default one hour without activity and twelve hours after sign-in). trove then shows **Your session has ended**:

1. Select **Sign in again** to sign in and return to the page you were on.
2. If you were editing something, **Copy draft as JSON** copies your unsaved entries first.
3. **Not now** closes the dialog without signing you in.

Some actions need a recent sign-in. trove then shows **Confirm it is you**. Select **Sign in again**, sign in, and repeat the action; the sign-in screen tells you why it appeared, and editors keep your entries.

### If the sign-in fails

When your identity provider signs you in but trove cannot complete the sign-in, a page **Sign-in failed** names a reason. **Start the sign-in again** returns to the sign-in screen.

| Reason | What it means | What to do |
|---|---|---|
| `flow_expired` | The sign-in took too long, or a page of it was opened twice | Start the sign-in again |
| `browser_mismatch` | The sign-in finished in another browser or profile than it started in | Start and finish it in the same browser |
| `user_disabled` | Your trove account is disabled | Ask an administrator |
| `not_provisioned` | The installation does not create accounts at the first sign-in, and you have none yet | Ask an administrator for an account |
| `not_in_allowed_group` | Your identity provider does not list you in a group that may sign in | Ask an administrator |
| `email_unverified` | Your identity provider has not verified your email address | Verify it at your identity provider |
| `provider_unavailable` | trove cannot reach the identity provider | Try again later |
| any other code | A configuration or provider problem | Tell an administrator the code and the time |

### Sign out

Open the user menu and select **Sign out**. trove ends your session, also in your other tabs, and shows **Signed out** with a **Sign in again** button. Depending on the installation, you may also be signed out at your identity provider. Your other browser sessions stay open; **Profile and tokens**, then **Sessions**, lists them and **End all other sessions** ends them.

## The screen layout

Every screen has a top bar, a side bar on the left, and the content area. At the top of the content, a breadcrumb trail shows where you are and links back to the screens above it. On a narrow screen the side bar is hidden and opens from the menu button.

### The top bar

From left to right:

| Element | What it does |
|---|---|
| Menu button | Opens the side bar when it is collapsed, or on a narrow screen |
| **trove** | Returns to the start page, **Browse** |
| **Find a repository** | Searches repositories by key and description and opens the matching list in the admin area's **Repositories**. Shown only if you may read the configuration of repositories |
| Project filter | Shown when you are a member of a project or may read every project: limits the repository and policy lists to one project |
| **Admin** | Opens the admin area. Shown only if you hold an administrative permission or are a member of a project |
| **Documentation** | Opens this manual. On a screen that has a matching manual page, it opens that page |
| **Theme** | **Follow the system**, **Light** or **Dark** |
| User menu | A round badge with your initial. Its menu shows your name and holds **Profile and tokens**, **Client setup**, **Language** and **Sign out** |

### The side bar

The side bar shows the area you are in:

| Area | Entries | Who sees it |
|---|---|---|
| **Artifacts** | **Packages**, **Browse**, **Guides**, **Vulnerabilities** | Everyone, anonymous readers included |
| **Admin area** | Projects, repositories, endpoints, Bastions, identity, access and system screens | Only people who hold an administrative permission or are a member of a project, and only the entries they may use. **Admin** in the top bar leads there, **trove** leads back |

The side bar is pinned on wide screens. **Collapse sidebar** at its foot hides it, and the menu button in the top bar brings it back; trove remembers the choice in this browser. On a narrow screen it opens over the content and closes with Escape, a click beside it, or when you pick an entry. Groups such as **Identity** fold and unfold with a click on their heading.

## Language and theme

trove is available in English and German.

1. Open the user menu and select **Language**.
2. Select **English** or **Deutsch**. trove saves the choice with your preferences and reloads the same page in that language.

The language is part of the page address (`/ui/en/` or `/ui/de/`), so a bookmark keeps it. When you open trove without a language in the address, it uses the language you saved in your preferences if you are signed in, otherwise the language your browser prefers, and English if that is neither. The sign-in page itself comes before any preference is known, so it follows the browser.

To change the colours, select **Theme** in the top bar and choose **Follow the system**, **Light** or **Dark**. When you are signed in, trove saves the choice with your preferences and applies it at your next sign-in, also in another browser. You can set both on your profile too: **Profile and tokens**, then **Profile**, section **Preferences**, then **Save preferences**.

## Where to go next

| To do this | Read |
|---|---|
| Understand local, remote and virtual repositories and their addresses | [Repositories](repositories.md) |
| Find a package, pick a version, copy its install command | [Packages](packages.md) |
| Browse, download, upload and delete files, in the browser or with curl | [Browsing and transferring files](files.md) |
| Configure npm, pnpm, Yarn or curl for a repository | [Connecting a package client](client-setup.md) |
| Create a token for a package manager or a CI job | [Personal access tokens](access-tokens.md) |
| Publish from a GitLab CI pipeline without a stored token | [GitLab CI pipelines](gitlab-ci-pipelines.md) |
| Understand an error message | [Errors and troubleshooting](troubleshooting.md) |
| Install, configure and operate trove | [How trove runs](../admin/overview.md) |

# dv gchat relay

dv in Google Chat. The relay is a Google Chat app's endpoint, run on Cloud Run,
that dv hubs connect out to, so a hub needs no public address:

- What you write to the app goes to your own hub. Your hub's replies are posted
  back in Google Chat as the app.
- In a space, several people can use the app in the same thread. Each person's
  messages go to their own hub, and a hub's questions are shown to its owner
  alone.

The relay never stores a message. It passes each one on as it arrives. Firestore
holds only:

- the paired hubs: a hash of each token, the owner's Chat user, name and email,
  and their direct-message space;
- pairing codes that are still waiting to be sent;
- which Chat thread belongs to which hub.

## Who can use it

- **The allow list.** `DV_RELAY_ALLOW` names the emails, and `@domains`, of
  people who may pair a hub or write to one. The relay won't start without it.
  Anyone else gets "This relay is not open to you."
- **Google's signature.** Every request to `/chat` must carry a token signed by
  Google for this app. Anything else is refused.
- **Pairing.** A hub asks for a code, and pairing completes only when an allowed
  person sends that code to the app in a direct message. The hub then gets a
  token. The relay keeps only its hash; dv keeps the token sealed on disk.
- **Each hub stays in its lane.** A hub receives only its owner's messages and
  button taps. It can post only in its owner's direct messages, or in threads
  its owner wrote to it in. It can change only messages it posted itself.
- **The app's visibility.** The Chat app can also be shown to named people only
  (see below).

## Setting it up

You need a Google Cloud project in your Workspace organization, and `gcloud`.
The commands run from the root of this repository.

```sh
PROJECT=my-project REGION=us-central1
PROJECT_NUMBER=$(gcloud projects describe $PROJECT --format='value(projectNumber)')
gcloud config set project $PROJECT
gcloud services enable chat.googleapis.com firestore.googleapis.com run.googleapis.com \
  cloudbuild.googleapis.com artifactregistry.googleapis.com
gcloud firestore databases create --location=$REGION

# The relay runs as its own service account. It posts to Chat as the app, and
# keeps its records in Firestore.
gcloud iam service-accounts create dv-gchat-relay
gcloud projects add-iam-policy-binding $PROJECT --role roles/datastore.user \
  --member serviceAccount:dv-gchat-relay@$PROJECT.iam.gserviceaccount.com

gcloud run deploy dv-gchat-relay --source . --region $REGION \
  --service-account dv-gchat-relay@$PROJECT.iam.gserviceaccount.com \
  --allow-unauthenticated --max-instances 1 \
  --set-build-env-vars GOOGLE_BUILDABLE=./cmd/dv-gchat-relay \
  --set-env-vars DV_RELAY_ALLOW=you@example.com,DV_RELAY_AUDIENCE=$PROJECT_NUMBER
```

`--allow-unauthenticated` is needed because both Google Chat and your hubs call
the relay, and the relay checks each of them itself. `--max-instances 1` is
needed because events on their way to a hub are held in memory. The deploy
prints the service's URL.

`DV_RELAY_AUDIENCE` is how the relay knows a request really comes from your
Chat app. Anyone can send a request to `/chat`, and nothing in the event itself
is signed, so Google Chat adds a token it signs. The token names whom it is
for, its audience. The relay accepts a request only when that audience is
`DV_RELAY_AUDIENCE`. Google signs tokens for every Chat app, so without this
check, tokens meant for someone else's app would get through.

The audience is whatever the Chat app's **Authentication audience** setting
(below) says, so the two must match:

- **Project Number**, used here: the project's number, known before the first
  deploy. Every Chat app in the project gets tokens with it, so use a project
  with no other Chat app.
- **HTTP endpoint URL**: the service's URL plus `/chat`. It is narrower, but
  the URL is only known once the service is deployed.

If they don't match, the relay turns every request away, and its logs say
"refused a request not from Google Chat".

Then configure the Chat app. In the Cloud console, go to **APIs & Services →
Google Chat API → Configuration**:

- **App name, avatar URL and description:** whatever you like, e.g. "dv".
- **Build this Chat app as a Google Workspace add-on:** untick it. The relay
  takes Chat's own events.
- **Functionality:** tick "Join spaces and group conversations" to use it in
  spaces as well as in direct messages.
- **Connection settings:** HTTP endpoint URL, set to the service's URL plus
  `/chat`.
- **Authentication audience:** Project Number, to match `DV_RELAY_AUDIENCE`.
- **Slash commands** (optional): `/sessions`, `/new`, `/stop`, `/last`,
  `/model`, `/close`, `/help` and `/hub`, with any IDs. Without them, typing the command
  as text works just as well.
- **Visibility:** "Make this Chat app available to specific people and groups
  in your domain". Add yourself and whoever else is in `DV_RELAY_ALLOW`.
  - Your Workspace admin must allow users to install Chat apps.

Finally, pair each hub:

1. In dv, open **Settings → Chat apps → Google Chat**.
2. Enter the service's URL and click **Connect**.
3. In Google Chat, start a chat with the app, and send it the code dv shows:
   `connect ABCD-2345`.

## Using it

- **Direct messages:** write what a session is to do, and dv starts one. The
  session's replies arrive in that message's thread. Write in the thread to
  send the session more.
  - Start a message with a folder and a colon to choose where the session runs,
    as with Telegram, e.g. `notes wt: …`. `/help` lists the rest.
- **Spaces:** mention the app with what the session is to do, and it starts in
  that thread. The session posts its progress there, where everyone in the space
  can read it. Anything meant only for you is shown to you alone: its prompts,
  lists such as `/sessions`, the `/model` picker, `/help`, and questions about
  where to start.
  - `/new` works only in direct messages. In a space, mentioning the app with a
    message is the way to start a session.
  - The app sees only messages that mention it. It never reads what else is in
    the thread.
- **Several dvs:** pair each one the same way. New sessions start on the one
  you paired last. `/hub` lists your dvs; tap one to start new sessions there
  instead. A session's thread stays with the dv it began on.

What a Chat app can't do:

- React to messages, or show that it is typing. `/stop` is answered in words
  instead.
- Send more than about one message a second to a space. The relay waits and
  retries when Chat asks it to slow down.

If the app answers "This relay is not open to you" when you are on the allow
list, look in the service's logs. They record whom the relay turned away, by
Chat user and email; an empty email means Chat did not send one.

## Trying it out

`DV_RELAY_STORE=memory` keeps hubs in memory, where they are lost when the
relay stops. Posting to Chat needs the app's credentials, so the relay only
works fully on Cloud Run.

```sh
make relay
DV_RELAY_STORE=memory DV_RELAY_UNVERIFIED=1 DV_RELAY_ALLOW=you@example.com ./dv-gchat-relay
```

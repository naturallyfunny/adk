// Package spotify exposes a Spotify client as a set of ADK tools, giving an
// agent its own hands on the music: hearing what is playing, finding tracks,
// and driving playback on the human's devices.
package spotify

import (
	"errors"

	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"go.naturallyfunny.dev/spotify"
)

type trackView struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Artists []string `json:"artists"`
	URI     string   `json:"uri"`
	URL     string   `json:"url"`
}

type playlistView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Total       int    `json:"total"`
	URL         string `json:"url"`
}

type deviceView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	IsActive bool   `json:"is_active"`
	Volume   int    `json:"volume"`
}

// playbackView reports what is playing right now. Track and Device are omitted
// when nothing identifiable is loaded or no device is active, so Playing alone
// answers "is anything sounding?".
type playbackView struct {
	Playing     bool        `json:"playing"`
	Track       *trackView  `json:"track,omitempty"`
	Device      *deviceView `json:"device,omitempty"`
	ProgressMs  int         `json:"progress_ms,omitempty"`
	ContextURI  string      `json:"context_uri,omitempty"`
	ContextType string      `json:"context_type,omitempty"`
}

// noArgs is shared by the tools that take no input.
type noArgs struct{}

type searchTracksArgs struct {
	Query string `json:"query"`
}

type playlistTracksArgs struct {
	PlaylistID string `json:"playlist_id"`
}

type playArgs struct {
	URI        string `json:"uri"`
	ContextURI string `json:"context_uri"`
	DeviceID   string `json:"device_id"`
}

type transferPlaybackArgs struct {
	DeviceID string `json:"device_id"`
	Play     bool   `json:"play"`
}

type setVolumeArgs struct {
	Percent int `json:"percent"`
}

type tracksOutput struct {
	Tracks []trackView `json:"tracks"`
}

type playlistsOutput struct {
	Playlists []playlistView `json:"playlists"`
}

type devicesOutput struct {
	Devices []deviceView `json:"devices"`
}

// ack confirms a playback command landed. A returned error means it did not;
// ack{OK: true} means it did.
type ack struct {
	OK bool `json:"ok"`
}

// Tools returns the Spotify toolset bound to c. It errors if c is nil so the
// failure surfaces at wiring time, not on the first tool call.
func Tools(c *spotify.Client) ([]adktool.Tool, error) {
	if c == nil {
		return nil, errors.New("adk: Tools: client must not be nil")
	}

	searchTracks, err := functiontool.New(
		functiontool.Config{
			Name: "search_tracks",
			Description: `Find tracks on Spotify by name, artist, lyric, or vibe. The first step before I can put something on.

WHEN TO USE:
- I want to play a specific song but need its uri first
- The human names a song, artist, or mood and I go looking for it
- I'm picking something to play and want options to choose from

HOW TO USE:
- query: free text, e.g. "bohemian rhapsody", "miles davis kind of blue", or "rainy day jazz".
- Each result carries a uri — hand that to play to actually start it.`,
		},
		func(toolCtx adktool.Context, in searchTracksArgs) (tracksOutput, error) {
			tracks, err := c.SearchTracks(toolCtx, toolCtx.UserID(), in.Query)
			if err != nil {
				return tracksOutput{}, forAgent(err)
			}
			return tracksOutput{Tracks: toTrackViews(tracks)}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	myPlaylists, err := functiontool.New(
		functiontool.Config{
			Name: "my_playlists",
			Description: `Look over the human's own Spotify playlists.

WHEN TO USE:
- The human asks me to play one of their playlists ("put on my focus mix")
- I want to know what collections they keep before suggesting one
- I need a playlist's uri to play it whole, or its id to look inside with playlist_tracks`,
		},
		func(toolCtx adktool.Context, _ noArgs) (playlistsOutput, error) {
			playlists, err := c.UserPlaylists(toolCtx, toolCtx.UserID())
			if err != nil {
				return playlistsOutput{}, forAgent(err)
			}
			return playlistsOutput{Playlists: toPlaylistViews(playlists)}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	playlistTracks, err := functiontool.New(
		functiontool.Config{
			Name: "playlist_tracks",
			Description: `Look inside a playlist to see the tracks it holds.

WHEN TO USE:
- I want to know what's in a playlist before playing it, or pick one track from it
- The human asks what's on a given playlist

HOW TO USE:
- playlist_id: from my_playlists or search results. To just play the whole
  playlist, I don't need this — I can pass its uri straight to play.`,
		},
		func(toolCtx adktool.Context, in playlistTracksArgs) (tracksOutput, error) {
			tracks, err := c.PlaylistTracks(toolCtx, toolCtx.UserID(), in.PlaylistID)
			if err != nil {
				return tracksOutput{}, forAgent(err)
			}
			return tracksOutput{Tracks: toTrackViews(tracks)}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	nowPlaying, err := functiontool.New(
		functiontool.Config{
			Name: "now_playing",
			Description: `Hear what's playing right now — the current track, device, and how far in it is.

WHEN TO USE:
- The human asks what's on, or I want to know before I change anything
- I'm about to pause, skip, or comment on the music and want the current state

WHAT I GET BACK:
- playing: false means nothing is sounding (paused or no active device)
- track/device are present only when something is actually loaded and active`,
		},
		func(toolCtx adktool.Context, _ noArgs) (playbackView, error) {
			pb, err := c.CurrentPlayback(toolCtx, toolCtx.UserID())
			if err != nil {
				return playbackView{}, forAgent(err)
			}
			return toPlaybackView(pb), nil
		},
	)
	if err != nil {
		return nil, err
	}

	myDevices, err := functiontool.New(
		functiontool.Config{
			Name: "my_devices",
			Description: `See where I can play — the human's available Spotify devices (phone, laptop, speaker).

WHEN TO USE:
- play reported no active device and I need to find one to target
- The human asks where music can play, or to move playback to a specific device
- I want a device_id to pass to play`,
		},
		func(toolCtx adktool.Context, _ noArgs) (devicesOutput, error) {
			devices, err := c.Devices(toolCtx, toolCtx.UserID())
			if err != nil {
				return devicesOutput{}, forAgent(err)
			}
			return devicesOutput{Devices: toDeviceViews(devices)}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	play, err := functiontool.New(
		functiontool.Config{
			Name: "play",
			Description: `Put music on, or resume what's loaded. My way of actually starting the sound.

WHEN TO USE:
- I've found something with search_tracks/my_playlists and want to play it
- The human asks to play a song, album, artist, or playlist
- Playback is paused and I want to pick it back up

HOW TO USE:
- uri: the Spotify uri of what to play. A track uri plays that one song; an
  album, artist, or playlist uri plays the whole thing. Leave uri empty to
  resume whatever is already loaded — that's how I un-pause.
- context_uri: an album/playlist/artist to play WITHIN. Set this together with
  a track uri when the human picks a track that came from a playlist (or album)
  and wants skip next/previous to stay inside it: pass the playlist as
  context_uri and the track as uri. Playback starts at that track but keeps the
  playlist as context. Leave context_uri empty for a plain single track or to
  play a whole thing.
- device_id: leave empty to use the active device. Set it (from my_devices) to
  start playback on a specific device.
- No active device? Don't give up and don't ask the human yet. Call my_devices:
  if it lists ANY available device, retry play with that device's device_id —
  an available device can be idle and still be targeted, no human action needed.
  Only ask the human to open Spotify when my_devices comes back empty. To move an
  already-playing session onto an idle device while keeping its queue/position,
  prefer transfer_playback.

Premium-only, like all playback control.`,
		},
		func(toolCtx adktool.Context, in playArgs) (ack, error) {
			req := spotify.PlayRequest{
				DeviceID:   in.DeviceID,
				ContextURI: in.ContextURI,
				URI:        in.URI,
			}
			if err := c.Play(toolCtx, toolCtx.UserID(), req); err != nil {
				return ack{}, forAgent(err)
			}
			return ack{OK: true}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	transferPlayback, err := functiontool.New(
		functiontool.Config{
			Name: "transfer_playback",
			Description: `Move the current playback session onto another device, carrying the queue and
position across. How I wake an idle device without restarting the music.

WHEN TO USE:
- The human asks to move the music to another device ("play this on the living
  room speaker instead")
- A session is loaded but sitting on a device I want to hand off, keeping right
  where it is — unlike play, this doesn't start fresh content

HOW TO USE:
- device_id: the target device from my_devices. It only has to be available
  (open somewhere); it can be idle and still receive the session.
- play: true to make sure it's playing after the move, false to keep the current
  play/pause state.

To start specific new content on a device instead, use play with its device_id.
Premium-only, like all playback control.`,
		},
		func(toolCtx adktool.Context, in transferPlaybackArgs) (ack, error) {
			if err := c.TransferPlayback(toolCtx, toolCtx.UserID(), in.DeviceID, in.Play); err != nil {
				return ack{}, forAgent(err)
			}
			return ack{OK: true}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	pause, err := functiontool.New(
		functiontool.Config{
			Name: "pause",
			Description: `Pause playback, holding the spot. Resume later with play (empty uri).

WHEN TO USE:
- The human asks to stop or hold the music
- I want a moment of quiet — a call coming in, someone's talking`,
		},
		func(toolCtx adktool.Context, _ noArgs) (ack, error) {
			if err := c.Pause(toolCtx, toolCtx.UserID()); err != nil {
				return ack{}, forAgent(err)
			}
			return ack{OK: true}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	skipNext, err := functiontool.New(
		functiontool.Config{
			Name: "skip_next",
			Description: `Skip to the next track in the queue.

WHEN TO USE:
- The human doesn't want the current song, or asks for the next one
- I'm moving the listening along`,
		},
		func(toolCtx adktool.Context, _ noArgs) (ack, error) {
			if err := c.Next(toolCtx, toolCtx.UserID()); err != nil {
				return ack{}, forAgent(err)
			}
			return ack{OK: true}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	skipPrevious, err := functiontool.New(
		functiontool.Config{
			Name: "skip_previous",
			Description: `Go back to the previous track.

WHEN TO USE:
- The human wants the song before this one, or to hear that again`,
		},
		func(toolCtx adktool.Context, _ noArgs) (ack, error) {
			if err := c.Previous(toolCtx, toolCtx.UserID()); err != nil {
				return ack{}, forAgent(err)
			}
			return ack{OK: true}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	setVolume, err := functiontool.New(
		functiontool.Config{
			Name: "set_volume",
			Description: `Set playback volume.

WHEN TO USE:
- The human asks to turn it up or down, or to a level
- The moment calls for quieter or louder

HOW TO USE:
- percent: 0 (silent) to 100 (full). Premium-only.`,
		},
		func(toolCtx adktool.Context, in setVolumeArgs) (ack, error) {
			if err := c.SetVolume(toolCtx, toolCtx.UserID(), in.Percent); err != nil {
				return ack{}, forAgent(err)
			}
			return ack{OK: true}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	return []adktool.Tool{
		searchTracks,
		myPlaylists,
		playlistTracks,
		nowPlaying,
		myDevices,
		play,
		transferPlayback,
		pause,
		skipNext,
		skipPrevious,
		setVolume,
	}, nil
}

// forAgent rewrites the client's sentinel errors into guidance the agent can
// act on, while leaving anything unrecognized untouched. The point is that the
// model reading the error should know what to do next.
func forAgent(err error) error {
	switch {
	case errors.Is(err, spotify.ErrNotConnected):
		return errors.New("the human hasn't connected their Spotify account yet — they need to link it before I can do this")
	case errors.Is(err, spotify.ErrNoActiveDevice):
		return errors.New("no active Spotify device — ask the human to open Spotify somewhere, or check my_devices, then try again")
	case errors.Is(err, spotify.ErrPremiumRequired):
		return errors.New("this needs Spotify Premium; the human's account can't do playback control")
	case errors.Is(err, spotify.ErrRateLimited):
		return errors.New("Spotify is rate-limiting right now — wait a moment before trying again")
	default:
		return err
	}
}

func toTrackView(t spotify.Track) trackView {
	return trackView{
		ID:      t.ID,
		Name:    t.Name,
		Artists: t.Artists,
		URI:     t.URI,
		URL:     t.URL,
	}
}

func toTrackViews(tracks []spotify.Track) []trackView {
	views := make([]trackView, len(tracks))
	for i, t := range tracks {
		views[i] = toTrackView(t)
	}
	return views
}

func toPlaylistViews(playlists []spotify.Playlist) []playlistView {
	views := make([]playlistView, len(playlists))
	for i, p := range playlists {
		views[i] = playlistView{
			ID:          p.ID,
			Name:        p.Name,
			Description: p.Description,
			Total:       p.Total,
			URL:         p.URL,
		}
	}
	return views
}

func toDeviceView(d spotify.Device) deviceView {
	return deviceView{
		ID:       d.ID,
		Name:     d.Name,
		Type:     d.Type,
		IsActive: d.IsActive,
		Volume:   d.Volume,
	}
}

func toDeviceViews(devices []spotify.Device) []deviceView {
	views := make([]deviceView, len(devices))
	for i, d := range devices {
		views[i] = toDeviceView(d)
	}
	return views
}

// toPlaybackView maps the client's playback snapshot to the agent-facing view.
// A nil snapshot means no active device, which reads as simply not playing.
func toPlaybackView(pb *spotify.Playback) playbackView {
	if pb == nil {
		return playbackView{Playing: false}
	}
	view := playbackView{
		Playing:     pb.IsPlaying,
		ProgressMs:  pb.ProgressMs,
		ContextURI:  pb.ContextURI,
		ContextType: pb.ContextType,
	}
	if pb.Track != nil {
		t := toTrackView(*pb.Track)
		view.Track = &t
	}
	d := toDeviceView(pb.Device)
	view.Device = &d
	return view
}

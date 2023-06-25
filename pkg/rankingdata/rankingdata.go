package rankingdata

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"discord_ladder_bot/pkg/config"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type RankingData struct {
	Version  string                `bson:"version,omitempty"`
	Channels []*ChannelRankingData `bson:"channels"`
	conf     *config.Config
	mutex    sync.Mutex
}

type ChannelRankingData struct {
	ChannelID        string          `bson:"channel_id"`
	ChallengeMode    string          `bson:"challenge_mode"`
	RankedPlayers    []Player        `bson:"ranked_players"`
	ActiveChallenges []Challenge     `bson:"active_challenges"`
	ResultHistory    []ResultHistory `bson:"result_history"`
	mutex            sync.Mutex
}

type Player struct {
	PlayerID        string `bson:"player_id"`
	Status          string `bson:"status,omitempty"`
	Position        int    `bson:"position"`
	TimeZone        string `bson:"time_zone,optional,omitempty"`
	PrefferedServer string `bson:"preffered_server,omitempty"`
}

type Challenge struct {
	ChallengerID      string    `bson:"challenger_id"`
	ChallengeeID      string    `bson:"challengee_id"`
	ChallengeDate     time.Time `bson:"challenge_date"`
	ChallengeDeadline time.Time `bson:"challenge_deadline"`
}

type ResultHistory struct {
	Challenger    string    `bson:"challenger"`
	Challengee    string    `bson:"challengee"`
	Result        string    `bson:"result"`
	ChallengeDate time.Time `bson:"challenge_date,omitempty"`
	ResolveDate   time.Time `bson:"resolve_date,omitempty"`
}

// function that reads a mongodb and returns a RankingData struct
func ReadRankingData(conf *config.Config) (*RankingData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(conf.MongoURI))
	if err != nil {
		return nil, err
	}
	defer client.Disconnect(ctx)

	rankingData := RankingData{conf: conf}
	rankingData.Channels = make([]*ChannelRankingData, 0)

	db := client.Database(conf.MongoDBName)
	collection := db.Collection(conf.MongoCollectionName)
	cursor, err := collection.Find(ctx, bson.M{})
	if err != nil {
		fmt.Println("Warning: No ranking data found, creating new collection")
		// return an empty ranking data
		return &rankingData, nil
	}
	defer cursor.Close(ctx)

	for cursor.Next(ctx) {
		channelRankingData := ChannelRankingData{}
		err := cursor.Decode(&channelRankingData)
		if err != nil {
			return nil, err
		}
		rankingData.Channels = append(rankingData.Channels, &channelRankingData)
	}

	return &rankingData, nil
}

// function that writes a RankingData struct to a mongodb
func (rankingData *RankingData) Write() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(rankingData.conf.MongoURI))
	if err != nil {
		return err
	}
	defer client.Disconnect(ctx)

	collection := client.Database(rankingData.conf.MongoDBName).Collection(rankingData.conf.MongoCollectionName)
	// delete all documents in the collection, ignore errors for empty results
	_ = collection.Drop(ctx)

	// insert each channel into the collection
	for i := range rankingData.Channels {
		channel := &rankingData.Channels[i]
		_, err := collection.InsertOne(ctx, channel)
		if err != nil {
			return err
		}
	}

	return nil
}

//
// Utility/Private functions
// NOTE: These functions are not thread safe and should only be called from
// 	 within a function that has already locked the mutex.
//

// function that finds a channel in a RankingData struct
func (rankingData *RankingData) findChannel(channelID string) (*ChannelRankingData, error) {
	for i := range rankingData.Channels {
		channel := rankingData.Channels[i]
		if channel.ChannelID == channelID {
			return channel, nil
		}
	}
	return nil, errors.New("channel not found")
}

// function that finds a player in a RankingData channel struct
func (channel *ChannelRankingData) findPlayer(playerID string) (*Player, error) {
	for i := range channel.RankedPlayers {
		player := &channel.RankedPlayers[i]
		if player.PlayerID == playerID {
			return player, nil
		}
	}
	return nil, errors.New("player not found")
}

// function that finds a challenge in a RankingData channel struct
func (channel *ChannelRankingData) findChallenge(playerID string) (*Challenge, error) {
	for i := range channel.ActiveChallenges {
		challenge := &channel.ActiveChallenges[i]
		if challenge.ChallengerID == playerID || challenge.ChallengeeID == playerID {
			return challenge, nil
		}
	}
	return nil, errors.New("challenge not found")
}

// function that determines if a player is available for a challenge
func (channel *ChannelRankingData) isPlayerAvailable(playerID string) bool {
	_, err := channel.findChallenge(playerID)
	player, _ := channel.findPlayer(playerID)

	// return true if the player is not in a challenge and is active
	return err != nil && player.Status == "active"
}

func tierFromPos(position int) int {
	tier := 1
	tierdiv := 1
	for tierdiv < position {
		tier++
		tierdiv += tier
	}
	return tier
}

// maxPosInTier returns the maximum position in a tier.
func maxPosInTier(tier int) int {
	t := 1
	pos := 1
	for t < tier {
		pos += t
		t++
	}
	return pos
}

// Position sorting functions
type byPosition []Player

func (a byPosition) Len() int           { return len(a) }
func (a byPosition) Less(i, j int) bool { return a[i].Position < a[j].Position }
func (a byPosition) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

// private function that sorts the players by position and fixes any gaps
func (channel *ChannelRankingData) fixPositions() {

	// sort the players by position
	sort.Sort(byPosition(channel.RankedPlayers))

	// remove any gaps in the positions
	for i := range channel.RankedPlayers {
		channel.RankedPlayers[i].Position = i + 1
	}
}

//
// Public functions
//

// function that prints a RankingData struct
func (rankingData *RankingData) PrintRaw() (string, error) {
	//lock all the mutexes
	rankingData.mutex.Lock()
	defer rankingData.mutex.Unlock()

	for i := range rankingData.Channels {
		channel := rankingData.Channels[i]
		channel.mutex.Lock()
		defer channel.mutex.Unlock()
	}

	bsonBytes, err := bson.Marshal(rankingData)
	if err != nil {
		return "", err
	}
	return string(bsonBytes), err
}

// function that adds a new channel to the ranking data
func (rankingData *RankingData) AddChannel(channelID string) error {
	rankingData.mutex.Lock()
	defer rankingData.mutex.Unlock()

	// return an error if the channel is already present
	if _, err := rankingData.findChannel(channelID); err == nil {
		return errors.New("channel is already registered")
	}

	// add the channel to the ranking data
	rankingData.Channels = append(rankingData.Channels,
		&ChannelRankingData{
			ChannelID:        channelID,
			ChallengeMode:    "ladder",
			RankedPlayers:    []Player{},
			ActiveChallenges: []Challenge{},
			ResultHistory:    []ResultHistory{},
		})
	return nil
}

// function that removes a channel from the ranking data
func (rankingData *RankingData) RemoveChannel(channelID string) error {
	rankingData.mutex.Lock()
	defer rankingData.mutex.Unlock()

	// return an error if the channel is not present
	if _, err := rankingData.findChannel(channelID); err != nil {
		return err
	}

	// remove the channel from the ranking data
	for i := range rankingData.Channels {
		if rankingData.Channels[i].ChannelID == channelID {
			rankingData.Channels = append(rankingData.Channels[:i], rankingData.Channels[i+1:]...)
			return nil
		}
	}
	return errors.New("channel not found")
}

// function that finds a channel in a RankingData struct
func (rankingData *RankingData) FindChannel(channelID string) (*ChannelRankingData, error) {
	rankingData.mutex.Lock()
	defer rankingData.mutex.Unlock()

	return rankingData.findChannel(channelID)
}

// function that adds a new player to the ranking data channel
func (channel *ChannelRankingData) AddPlayer(playerID string) error {
	channel.mutex.Lock()
	defer channel.mutex.Unlock()

	// return an error if the player is already present
	if _, err := channel.findPlayer(playerID); err == nil {
		return errors.New("Player is already registered")
	}

	// add the player to the ranking data
	channel.RankedPlayers = append(channel.RankedPlayers,
		Player{
			PlayerID: playerID,
			Status:   "active",
			Position: len(channel.RankedPlayers) + 1,
		})
	return nil
}

// function that removes a player from the ranking data channel
func (channel *ChannelRankingData) RemovePlayer(playerID string) error {
	channel.mutex.Lock()
	defer channel.mutex.Unlock()

	// return an error if the player is not present
	removedPos := 0
	for i := range channel.RankedPlayers {
		player := &channel.RankedPlayers[i]
		if player.PlayerID == playerID {
			removedPos = player.Position
			channel.RankedPlayers = append(channel.RankedPlayers[:i], channel.RankedPlayers[i+1:]...)
			break
		}
	}
	if removedPos == 0 {
		return errors.New("player not found")
	}

	// decrement the position of all players below the removed player
	channel.fixPositions()

	// remove any active challenges that the player is in
	for i := range channel.ActiveChallenges {
		challenge := &channel.ActiveChallenges[i]
		if challenge.ChallengerID == playerID || challenge.ChallengeeID == playerID {
			channel.ActiveChallenges = append(channel.ActiveChallenges[:i], channel.ActiveChallenges[i+1:]...)
			break
		}
	}

	return nil
}

// function that moves a player to a new position
// NOTE: this function does not check if the move will cause invalid challenges
func (channel *ChannelRankingData) MovePlayer(playerID string, newPosition int) error {
	channel.mutex.Lock()
	defer channel.mutex.Unlock()

	// return an error if the player is not present
	movingPlayer, err := channel.findPlayer(playerID)
	if err != nil {
		return errors.New("player not found")
	}

	// return error if the player is in a challenge
	if _, err := channel.findChallenge(playerID); err == nil {
		return errors.New("player is in a challenge")
	}

	for i := range channel.RankedPlayers {
		player := &channel.RankedPlayers[i]
		if player.Position >= newPosition {
			player.Position++
		}
	}
	movingPlayer.Position = newPosition

	return nil
}

// function that starts a challenge
func (channel *ChannelRankingData) StartChallenge(challengerID string, challengeeID string) error {
	channel.mutex.Lock()
	defer channel.mutex.Unlock()

	// if the challenger is not registered, return an error
	challenger, err := channel.findPlayer(challengerID)
	if err != nil {
		return errors.New("challenger not found")
	}
	challengee, err := channel.findPlayer(challengeeID)
	if err != nil {
		return errors.New("challengee not found")
	}

	// if the challenger is not available, return an error
	// TODO: it would be good to make the reasoning for the error more specific
	if !channel.isPlayerAvailable(challengerID) {
		return errors.New("challenger is not available")
	}
	if !channel.isPlayerAvailable(challengeeID) {
		return errors.New("challengee is not available")
	}

	// determine if the challenger is eligible to challenge challengee
	challengerTier := tierFromPos(challenger.Position)
	challengeeTier := tierFromPos(challengee.Position)

	switch channel.ChallengeMode {
	// in linear/ladder mode, the challenger can only challenge the next person up
	case "linear", "ladder":
		if challenger.Position-1 != challengee.Position {
			return errors.New("challenger may only challenge the next person up")
		}
	// in pyramid mode, the challenger can only challenge someone in the same tier or the tier below
	case "pyramid":
		if challenger.Position > challengee.Position && challengerTier-1 <= challengeeTier {
			return errors.New("challenger is not eligible to challenge challengee")
		}
	default:
		return errors.New("invalid challenge mode")
	}

	// create the challenge
	channel.ActiveChallenges = append(channel.ActiveChallenges,
		Challenge{
			ChallengerID:      challengerID,
			ChallengeeID:      challengeeID,
			ChallengeDate:     time.Now(),
			ChallengeDeadline: time.Now().Add(time.Hour * 24 * 7),
		})
	return nil
}

// function that resolves a challenge
func (channel *ChannelRankingData) ResolveChallenge(reporterID string, action string) error {
	channel.mutex.Lock()
	defer channel.mutex.Unlock()

	// find the challenge
	challenge, err := channel.findChallenge(reporterID)
	if err != nil {
		return errors.New("challenge not found")
	}

	// sanity check the action
	switch action {
	case "won", "lost", "cancel", "forfiet", "timed out":
		// do nothing
	default:
		return errors.New("invalid action")
	}

	// if the reporter is the challengee, reverse the result/action
	if reporterID == challenge.ChallengeeID {
		switch action {
		case "won":
			action = "lost"
		case "lost":
			action = "won"
		case "cancel":
			return errors.New("challengee cannot cancel, only forfiet")
		}
	} else if reporterID == challenge.ChallengerID {
		if action == "forfiet" {
			return errors.New("challenger cannot forfiet, only cancel")
		}
	}

	// add the result to the history
	channel.ResultHistory = append(channel.ResultHistory,
		ResultHistory{
			Challenger:    challenge.ChallengerID,
			Challengee:    challenge.ChallengeeID,
			Result:        action,
			ChallengeDate: challenge.ChallengeDate,
			ResolveDate:   time.Now(),
		})

	// if the challenger won (or the match was conceded or timed out), update the ranking
	if action == "won" || action == "forfiet" || action == "timed out" {
		challenger, err := channel.findPlayer(challenge.ChallengerID)
		if err != nil {
			return errors.New("challenger not found")
		}
		challengee, err := channel.findPlayer(challenge.ChallengeeID)
		if err != nil {
			return errors.New("challengee not found")
		}
		challenger.Position, challengee.Position = challengee.Position, challenger.Position
		channel.fixPositions()
	}

	// remove the challenge
	for i := range channel.ActiveChallenges {
		challenge := &channel.ActiveChallenges[i]
		if challenge.ChallengerID == reporterID || challenge.ChallengeeID == reporterID {
			channel.ActiveChallenges = append(channel.ActiveChallenges[:i], channel.ActiveChallenges[i+1:]...)
			break
		}
	}

	return nil
}

<style lang="less">
  @import "./home.less";
  @import "../../styles/common.less";

  .fuc {
  }

  .fuc li {
    margin-top: 2%;
    margin-left: 15%;
  }

  .fuc h4 {
    margin-top: 2%;
    margin-left: 10%;
  }

  .fuc h3 {
  }
</style>
<template>
  <div class="home-main">
    <Row>
      <Col span="8">
        <Row>
          <Card>
            <Row type="flex" class="user-infor">
              <Col span="8">
                <Row class-name="made-child-con-middle" type="flex" align="middle">
                  <img class="avator-img" src="../../assets/avatar.png"/>
                </Row>
              </Col>
              <Col span="16" style="padding-left:6px;">
                <Row class-name="made-child-con-middle" type="flex" align="middle">
                  <div>
                    <b class="card-user-infor-name">{{username}}</b>
                    <p>Go confidently in the direction.</p>
                  </div>
                </Row>
              </Col>
            </Row>
            <div class="line-gray"></div>
            <Row class="margin-top-8">
              <Col span="8">
                <p class="notwrap">登录时间:</p>
              </Col>
              <Col span="16" class="padding-left-8">{{time}}</Col>
            </Row>
            <Row>
              <Col span="8">
                <p class="notwrap">当前版本:</p>
              </Col>
              <Col span="16" class="padding-left-8"> <a target="_Blank"  href="https://cookiey.github.io/Yearning-document/update/">v1.4.7</a></Col>
            </Row>
          </Card>
        </Row>
        <Row class="margin-top-10">
          <Card>
            <p slot="title" class="card-title">
              <Icon type="md-person" size="24"/>
              个人信息
            </p>
            <userinfomation></userinfomation>
          </Card>
        </Row>
      </Col>
      <Col span="16" class-name="padding-left-5">
        <Row>
          <Col span="6">
            <infor-card id-name="user_created_count" :end-val="count.createUser" iconType="md-person-add"
                        color="#2d8cf0" intro-text="平台用户"></infor-card>
          </Col>
          <Col span="6" class-name="padding-left-5">
            <infor-card id-name="visit_count" :end-val="count.link" iconType="ios-eye" color="#64d572" :iconSize="50"
                        intro-text="数据库连接地址"></infor-card>
          </Col>
          <Col span="6" class-name="padding-left-5">
            <infor-card id-name="transfer_count" :end-val="count.order" iconType="md-shuffle" color="#f25e43"
                        intro-text="我提交的工单"></infor-card>
          </Col>
        </Row>
        <Row class="margin-top-10">
          <Col span="12">
            <Card>
              <p slot="title" class="card-title">
                <Icon type="android-map"></Icon>
                公告栏
              </p>
              <div class="data-sourcefunc-row">
                <H2>欢迎使用Yearning SQL 审核平台</H2>
                <br>
                <div class="fuc">
                  <H3>主要功能:</H3>
                  <H4 v-for="i in board.title" :key="i">{{i}}</H4>
                </div>
              </div>
            </Card>
          </Col>
          <Col span="12" class="padding-left-10">
            <Card>
              <p slot="title" class="card-title">
                <Icon type="ios-pulse-strong"></Icon>
                DDL & DML 工单提交统计
              </p>
              <div class="data-source-row">
                <data-source-pie></data-source-pie>
              </div>
            </Card>
          </Col>
        </Row>
      </Col>
    </Row>
  </div>
</template>

<script>
  import axios from 'axios'
  import dataSourcePie from './components/dataSourcePie.vue'
  import inforCard from './components/inforCard.vue'
  import toDoListItem from './components/toDoListItem.vue'
  import userinfomation from '../personalCenter/own-space'

  export default {
    components: {
      dataSourcePie,
      inforCard,
      toDoListItem,
      userinfomation

    },
    data () {
      return {
        toDoList: [{
          title: ''
        }],
        count: {
          createUser: 0,
          order: 0,
          link: 0
        },
        newToDoItemValue: '',
        time: '',
        username: sessionStorage.getItem('user'),
        board: {
          'title': ['1.DDL语句生成', '2.SQL语句审核及回滚', '3.工单流程化', '4.可视化数据查询', '5.细粒度的权限划分']
        }
      }
    },
    methods: {
      formatDate () {
        let date = new Date()
        let month = date.getMonth() + 1
        this.time = date.getFullYear() + '/' + month + '/' + date.getDate() + '  ' + date.getHours() + ':' + date.getMinutes() + ':' + date.getSeconds()
      }
    },
    mounted () {
      axios.get(`${this.$config.url}/homedata/infocard`)
        .then(res => {
          this.count.createUser = res.data[0]
          this.count.order = res.data[1]
          this.count.link = res.data[2]
        })
        .catch(error => {
          this.$config.err_notice(this, error)
        })
      this.formatDate()
    }
  }
</script>
